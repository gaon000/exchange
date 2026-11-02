# 스냅샷 생성의 중복 직렬화 최적화

2026-11-02, `codex/snapshot-size-preflight`에서 파일 생성 전 크기 검사를 JSON bytes 없는 길이 계산으로 바꿨다. **동일 상태에서 개선 전후 각각 5회 측정한 생성 시간 중앙값은 675.941ms → 475.324ms, 29.7% 감소했다.** 5쌍 중 4쌍이 빨라졌고 2회차 후보는 더 느렸다. 느린 표본도 포함했으며 모든 실행이 개선됐다고 해석하지 않는다. 파일 bytes와 저장·복구 검증이 일치하고 반복 중앙값·할당량이 줄어 후보를 채택했다.

기준은 decoder 개선까지 반영된 `df71229eabb9781c7eb1f2bb99e08eea1015ca38`다. 후보는 그 위의 미커밋 작업 트리다. 이전 보고서의 숫자를 기준 표본으로 재사용하지 않고 같은 harness로 두 버전을 새로 측정했다. [기계 판독 집계](20261102-snapshot-preflight-comparison.json), [원본 manifest](20261102-snapshot-preflight-artifacts.json), [저장 계약](../design-docs/storage-v1.md), [선행 단계 분석](snapshot-stage-breakdown.md)을 함께 읽는다.

## 변경과 계약

기존 `measure`는 실제 writer와 같은 순회로 각 leaf를 `json.Marshal`한 뒤 bytes를 버리고 길이만 셌다. 실제 파일 출력에서 직렬화가 다시 실행됐다. 후보는 [size.go](../../internal/snapshot/size.go)의 읽기 전용 길이 계산 계획으로 첫 번째 직렬화를 대체한다. 고정 DTO의 필드 순서·키 길이를 시작 시 계산하고, 실행 중에는 정수 자리 수, 문자열 escape, null, 선택 포인터만 센다. 공통 payload 순회와 실제 JSON writer를 유지한다.

지원하지 않는 schema/custom codec 또는 잘못된 UTF-8·음수 도메인 정수는 기존 Marshal로 돌아간다. fallback은 기존 입력 허용·문자열 처리와 오류 wrapping/우선순위를 보존한다. 예를 들어 한 leaf에 너무 긴 문자열과 음수 정수가 함께 있으면 기존처럼 정수의 Marshal 오류가 먼저 반환된다. leaf 한도를 넘은 길이는 포화 계산하지만 나머지 필드의 오류 검사는 계속한다.

| 유지한 계약 | 근거 |
| --- | --- |
| 파일 접근 전에 shape·값 16 KiB·payload 2 GiB 검사 | 기존 검사 위치 유지, 정확한 경계·출력/파일 접근 없음·오류 우선순위 테스트 |
| canonical JSON·필드 순서·정수 문자열·Reason/null | 실제 writer 유지, 표준 JSON 기준 8,000개 값 차등 검사와 fuzz, 독립 golden bytes |
| 전체 payload를 메모리에 보관하지 않음 | leaf 길이 계산, 32 KiB writer와 streaming 배열/FIFO 유지 |
| envelope/header/checksum·short write 처리 | 기존 writer와 독립 golden·I/O 오류 회귀 테스트 |
| strict file sync → close → exclusive rename → directory sync | 게시 코드 변경 없음, 실제 APFS·오류 주입·프로세스 종료 회귀 |
| 기존 파일 재확인·복구 검증·WAL 대조·fallback | Confirm의 사전 계산만 공유, 나머지 저장/복구 코드 유지 및 전체 회귀 |
| 구·신 버전 파일 호환성과 상태 동일성 | 10개 생성 파일의 원본 SHA 일치, 10회 재시작 및 양방향 버전 교차 복구 |

production 변경은 [codec.go](../../internal/snapshot/codec.go)의 크기 계산 연결과 새 `size.go`뿐이다. 도메인·reader·동기화 adapter·runtime·HTTP 코드는 바꾸지 않았고 dependency를 추가하지 않았다. `measure`는 Write/Publish/Confirm에서 공유하므로 기존 파일의 재확인도 이 계산을 사용한다.

## 측정 조건

- 이전 10분 실험의 **같은 최종 상태**를 사용했다. seq/최초 결과 51,385, 계정 4, 주문 45,280, 체결 12,117, 호가 가격 단계 매수 7/매도 5다.
- 매 실행마다 `.lock`, 796-byte Genesis, 13,626,452-byte WAL만 새 폴더로 복사했다. 시작 시 snapshot 파일이 없음을 검사하고 `runtime.Open`에서 51,385개 명령을 재생했다. **재확인 경로가 아닌 새 파일 생성**이며 `publish.encodeWrite` 1회, `publish.compareBytes` 없음도 확인했다.
- 새 프로세스에서 `Open` 뒤 `runtime.Snapshot` 호출 전체를 측정했다. 복사·사전 해시·Open·사후 해시·종료·별도 재시작은 생성 시간 밖이다. Snapshot은 owner 사본 생성·정렬과 worker의 검사·쓰기·동기화를 포함한다.
- 기준/후보 각 5회. 홀수 쌍은 기준→후보, 짝수 쌍은 후보→기준. 각 생성 뒤 같은 버전의 새 프로세스로 snapshot 복구와 재확인을 검증했다. 마지막에 기준 파일을 후보로, 후보 파일을 기준으로도 복구했다.
- Apple M3 Pro 11 CPU / 18 GiB RAM, macOS 15.8 arm64/APFS, Go 1.27.3, cgo=1. GOMAXPROCS=11, GOGC=100, GOMEMLIMIT=off, GODEBUG 미설정. 시작 시 `df` 가용 공간은 17 GiB였다.
- 테스트·프로파일러·부하 생성기를 함께 실행하지 않았다. OS 캐시는 비우지 않았으며 입력 복사·해시와 앞선 재시작이 캐시 상태에 영향을 줄 수 있다. 장비의 다른 백그라운드 작업은 통제하지 않았다.

생성 결과는 52,307,324 bytes이며 모든 표본의 SHA-256이 원본 `6d836cda051929a34551776a6892aa5bda478058ea16bd23a75b27b41a355abe`와 일치했다. 모든 재시작은 snapshotSeq=51,385, replayedCommands=0, skipped=[]였고 전체 canonical 상태를 다시 확인했다. 선택한 원본 4개 파일과 모든 run의 Genesis/WAL/lock도 변경되지 않았다.

## 결과

시간은 ms, 중앙값 뒤 괄호는 5회 최소~최대다. 감소율은 `1 - 후보 중앙값 / 기준 중앙값`이다.

| 항목 | 개선 전 | 개선 후 | 감소율 |
| --- | ---: | ---: | ---: |
| 전체 Snapshot | 675.941 (659.623~741.170) | 475.324 (446.587~1,086.188) | **29.7%** |
| publish.measure | 248.935 (241.637~269.159) | 51.277 (45.882~376.232) | **79.4%** |
| publish.encodeWrite | 361.926 (346.830~444.004) | 365.005 (343.897~577.346) | -0.9% |
| capture.copy | 15.395 (14.661~16.905) | 16.767 (12.785~22.224) | -8.9% |
| capture.sort | 31.854 (27.929~33.015) | 30.083 (29.021~91.172) | 5.6% |
| publish.fileSync | 8.134 (6.587~8.618) | 7.850 (6.651~12.796) | 3.5% |
| publish.directorySync | 3.399 (1.690~5.003) | 3.314 (2.234~5.786) | 2.5% |
| Snapshot 중 할당 bytes | 302.543 MB | 182.755 MB | **39.6%** |
| Snapshot 중 할당 횟수 | 8,217,850 | 4,219,040 | 48.7% |
| Snapshot 중 GC 횟수 | 5 (4~5) | 3 (3~4) | 40.0% |

MB는 1,000,000 bytes다. 할당량/횟수와 GC는 Snapshot 직전·직후 `runtime.MemStats`의 차이이며 Open의 누적 비용은 제외한다. 할당량은 RSS·최대 메모리·살아 있는 heap 크기가 아니다. 단계 시간은 Snapshot 내부이므로 전체와 중복 합산하지 않는다.

| 쌍 | 기준 Snapshot | 후보 Snapshot | 기준 measure | 후보 measure |
| --- | ---: | ---: | ---: | ---: |
| 1 | 675.941 | 612.948 | 269.159 | 56.382 |
| 2 | 659.623 | **1,086.188** | 254.922 | **376.232** |
| 3 | 723.857 | 458.335 | 248.935 | 51.277 |
| 4 | 741.170 | 475.324 | 241.637 | 48.338 |
| 5 | 670.098 | 446.587 | 245.562 | 45.882 |

2회차 후보는 정렬 91.172ms·크기 검사 376.232ms·실제 인코딩/쓰기 577.346ms 등 여러 구간이 함께 길어졌다. 원인은 이 계측만으로 확정하지 못했다. 이 표본을 제외하거나 재측정 값으로 교체하지 않았다. 5회만으로 일반적인 통계적 유의성, 최악 시간 또는 응답 시간 상한 준수를 주장하지 않는다.

전체 중앙값 감소 약 201ms는 크기 검사 감소 약 198ms와 일치하는 규모다. 실제 인코딩/쓰기 중앙값은 약 365ms로 남아 있으며 생성 시간의 주요 비용이다. owner의 상태 복사·정렬을 이동하거나 제거한 것은 아니다. 별도 재시작 Open 중앙값은 2,865.710ms → 2,663.248ms였으나 이번 주된 비교는 생성이고, 동일 사전 계산을 공유하는 Confirm 이외의 복구 경로를 최적화하지 않았다.

## 검증과 재현 근거

- [길이 차등 테스트](../../internal/snapshot/size_test.go): 모든 leaf 타입의 8,000개 결정적 값, 전체 payload 100개 × 6개 크기 제한, 문자열/정수/nullable 경계와 미지원 schema fallback, 파일 접근 전 거절을 확인했다.
- `FuzzPreflightMatchesJSON`: 설정 30초, 실제 31.256초 / 보고된 실행 9,893회, 실패 없음. 기존 snapshot 패키지 테스트도 통과했다. fuzz의 원본 터미널 스트림 대신 실행 명령·종료/횟수를 validation 기록에 남겼다.
- `bash scripts/ci.sh portable`: **195 tests / 18 packages**. `bash scripts/ci.sh native`: **227 tests / 18 packages**, 의도한 자식 helper skip 1개. 포맷·vet·빌드, race·APFS·loopback·저장/프로세스 복구를 포함한다. 이번 변경의 GitHub CI는 실행하지 않았다.
- 후보의 source hash를 검사해 전체 CI와 측정 뒤 코드가 동일함을 확인했다. 생성 10회·재시작 10회·양방향 교차 복구 2회 모두 성공했다. 느린 표본을 포함한 모든 원본을 보존했다.

원본은 `var/snapshot-preflight-20261102.ztj_vssr`에 있다. 기준/후보 binary·빌드 정보·source archive/hash, `preflight.patch`, 공통 `probe.go`, `compare.py`, `analyze.py`, 환경/provenance, 10개 run과 생성·재시작 출력, 교차 복구 출력, 전체 CI 원본을 포함한다. manifest 자신을 제외한 **124개 파일 / 672,502,918 bytes**이며 manifest SHA-256은 `cf5e0e116323962479d3c2553c14f15edb0c55259f140bba486bdd962375133a`다. 선택한 원본과 결과 파일 hash는 재집계 때도 재검증했다.

실행 명령은 `python3 var/snapshot-preflight-20261102.ztj_vssr/compare.py`, 재집계는 `python3 var/snapshot-preflight-20261102.ztj_vssr/analyze.py`다. probe는 각 소스 상태에서 `scripts/go build -o <variant>/probe <실험 폴더>/probe.go`로 빌드했다. source archive는 기준 commit 위에 덮어쓸 Go/스크립트 파일이며 공통 testdata는 기준 commit에 있다. 재측정은 새 고유 폴더에 harness/provenance와 두 바이너리를 준비하고 source hash·HEAD·환경을 갱신한다. 기존 results가 있으면 compare.py가 재실행을 거절한다.

동일 상태 하나의 생성 결과이며 동시 주문 p99·최대 지속 처리량·cold-cache·다른 장비로 확대하지 않는다. 실제 전원 장애 검증은 보류다. 원본 업로드·커밋·master 병합·push는 수행하지 않았다.

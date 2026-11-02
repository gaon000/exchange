# 복구 디코더 최적화 전후 비교

2026-10-30, `codex/snapshot-decoder-optimization`에서 snapshot 값의 반복 파싱과 임시 버퍼 할당을 줄였다. **동일 상태의 5쌍 모두 JSON 해석과 전체 복구가 개선됐고 전체 상태 bytes가 원본과 일치해 후보를 채택했다.** 기준 HEAD는 `3b5a6f5`이며 두 빌드 모두 이전 계측 변경을 포함한 미커밋 작업 트리에서 만들었다. 기준 소스·바이너리를 변경 전에 동결했으며, 이전 보고서 숫자를 개선 전 표본으로 재사용하지 않았다.

[기계 판독 집계](20261030-snapshot-decoder-comparison.json), [원본 manifest 요약](20261030-snapshot-decoder-artifacts.json), [저장 계약](../design-docs/storage-v1.md#5-snapshot-dto와-게시), [선행 단계 분석](snapshot-stage-breakdown.md)을 함께 읽는다.

## 변경과 유지한 계약

변경 전은 각 JSON 값에 대해 strictjson의 token walk, 재귀 shape 검사, typed Unmarshal을 수행하고 다시 Marshal해서 원본 bytes와 비교했다. 변경 후는 snapshot 내부의 닫힌 scalar/leaf 타입 집합에만 **UTF-8 검사 → typed Unmarshal → canonical Marshal의 완전한 bytes 대조**를 사용한다. 값 하나마다 새로 키우던 버퍼는 decoder 소유의 고정 16KiB 버퍼로 재사용한다. 비교 기준에서 production source가 달라진 파일은 `internal/snapshot/decode.go` 하나다.

canonical 대조는 필드 누락·중복·미지 필드·이름의 대소문자 변경·필드 순서·공백·비정규 escape가 있으면 일치할 수 없어 거절한다. 정수와 Reason은 기존 custom decoder가 그대로 검사한다. nullable pointer/Reason 이외의 null은 typed decode 실패 또는 canonical 불일치로 거절한다. 이 등가성은 leaf DTO에 map/slice/interface/RawMessage나 새로운 custom codec이 없다는 전제에 한정한다. generic 타입 집합과 스키마 검사 테스트가 그 전제를 고정한다. 일반적인 JSON decoder 최적화로 확대 적용하지 않았다.

| 계약 | 검증 근거 |
| --- | --- |
| 중복·누락·미지 필드·대소문자·순서·null 규칙 | 모든 leaf DTO의 중첩 필드를 변형해 기존 strictjson + canonical 대조와 비교 |
| 정수·UTF-8·canonical encoding | 기존 custom decoder, UTF-8 검사·독립 golden, 기존 malformed JSON 검사와 차등/fuzz |
| 값 16KiB·깊이 64·배열 수·streaming | 기존 bounded scanner/배열 parser 유지, 경계·큰 FIFO·truncation 검사 |
| 버퍼 소유권 | 다음 값 해석·버퍼 덮어쓰기 이후에도 이전 반환 값이 같은지 확인 |
| envelope·checksum·identity·파일 오류 | 기존 전체 reader·변경되는 backing reader·오류 주입 테스트 유지 |
| 상태 의미·WAL 전체 대조·fallback·strict sync | engine/runtime/저장/프로세스 복구 코드를 바꾸지 않고 전체 native 회귀 통과 |
| 저장 포맷·최초 처리 결과·상태 동일성 | 독립 golden bytes와 동일 실험 상태의 전체 canonical snapshot bytes 일치 |

오류 메시지의 세부 문구는 달라질 수 있지만 손상/identity/I/O 오류 분류와 fallback·기동 실패 결정은 유지한다. HTTP/WAL의 일반 strictjson decoder, snapshot 생성·게시, 계측 계약은 변경하지 않았다.

## 측정 조건

- 이전 10분 snapshot 실험의 최종 상태 하나를 사용했다. 순번/최초 결과 51,385, 계정 4, 주문 45,280, 체결 12,117, 호가 가격 단계 매수 7/매도 5.
- `.lock`, 796-byte Genesis, 13,626,452-byte WAL, 52,307,324-byte 최신 snapshot을 모든 실행에 동일하게 복사했다. snapshot SHA-256은 `6d836cda051929a34551776a6892aa5bda478058ea16bd23a75b27b41a355abe`다.
- Apple M3 Pro 11 CPU / 18GiB RAM, macOS 15.8 arm64/APFS, Go 1.27.3, cgo=1. GOMAXPROCS=11, GOGC=100, GOMEMLIMIT=off, GODEBUG 미설정.
- 기준·후보 각각 5회, 매번 새 프로세스와 새 run 사본. 홀수 회는 기준→후보, 짝수 회는 후보→기준으로 실행했다. 다른 테스트/프로파일을 동시에 실행하지 않았다.
- 복사·입력 해시는 측정 전에 수행했다. `runtime.Open` 호출만 복구 시간이며 HTTP listen/ready 시간은 포함하지 않는다. 복구 후 `runtime.Snapshot`으로 전체 상태를 다시 캡처해 기존 snapshot과 canonical bytes를 비교하고 정상 종료했다.
- 모든 실행의 snapshotSeq=51,385, replayedCommands=0, skipped=[]를 확인했다. 새 명령은 없지만 기존 WAL 전체를 대조한다. 10회 모두 생성 재확인 성공, 원본 snapshot/Genesis/WAL/lock SHA 일치를 확인했다.

OS 캐시를 비우지 않았고 사본 복사·해시가 캐시를 데울 수 있다. 같은 frozen 상태 하나의 측정이며 cold-cache, 동시 거래 p99, 상태 증가의 복잡도나 다른 장비의 결론으로 확대하지 않는다. 계측·OS 백그라운드 작업의 오버헤드도 포함한다.

## 전체 결과

시간은 ms, 중앙값 뒤 괄호는 5회 최소~최대다. 감소율은 `1 - 후보 중앙값 / 기준 중앙값`이다.

| 항목 | 개선 전 | 개선 후 | 감소율 |
| --- | ---: | ---: | ---: |
| 전체 runtime.Open | 3,767.219 (3,748.102~3,784.717) | 2,543.226 (2,534.376~2,588.314) | **32.5%** |
| load.decode | 2,142.072 (2,137.785~2,175.092) | 932.882 (927.010~960.391) | **56.4%** |
| decode.results | 1,422.361 (1,419.755~1,433.316) | 603.611 (598.661~613.261) | 57.6% |
| decode.orders | 584.163 (581.636~603.109) | 266.989 (265.826~285.074) | 54.3% |
| decode.trades | 119.175 (118.755~121.277) | 55.697 (55.436~57.267) | 53.3% |
| Open 직후 프로세스 누적 할당량 | 2,194.661 MB | 1,195.814 MB | **45.5%** |
| Open 직후 프로세스 누적 GC 횟수 | 164 (163~167) | 109 (108~111) | 33.5% |

MB는 1,000,000 bytes이며 누적 할당량은 RSS나 살아 있는 heap 크기가 아니다. `decode.results/orders/trades`는 `load.decode`에 포함되고 `load.decode`는 Open에 포함되므로 서로 중복 합산하지 않는다. 이전 보고서의 기준 3.934초와 이번 기준 3.767초 차이를 최적화 성과에 넣지 않았다.

| 쌍 | 기준 Open ms | 후보 Open ms | 기준 decode ms | 후보 decode ms |
| --- | ---: | ---: | ---: | ---: |
| 1 | 3,767.219 | 2,543.226 | 2,142.072 | 932.882 |
| 2 | 3,757.644 | 2,534.376 | 2,137.785 | 927.010 |
| 3 | 3,784.717 | 2,549.421 | 2,175.092 | 938.138 |
| 4 | 3,774.697 | 2,588.314 | 2,155.355 | 960.391 |
| 5 | 3,748.102 | 2,541.217 | 2,138.958 | 932.577 |

5쌍 모두 두 구간이 빨라졌고 후보 최대값이 기준 최소값보다 작았다. 이는 이번 조건에서의 반복 결과이며 5회만으로 일반적인 통계적 유의성을 주장하지 않는다.

## 유지되는 비용과 다음 경계

| 나머지 단계 | 기준 중앙값 ms | 후보 중앙값 ms |
| --- | ---: | ---: |
| WAL open·최초 검사 | 478.556 | 478.769 |
| engine rebuild·의미 검사 | 78.985 | 80.271 |
| WAL prefix 대조·최종 불변식 | 541.914 | 537.810 |
| snapshot canonical 재확인·sync | 487.015 | 487.533 |

전체 복구에서 약 1.224초가 줄어든 것은 주로 decode의 약 1.209초 감소로 설명된다. WAL 검사와 snapshot 재확인 비용은 그대로 남았다. 이번 결과는 WAL 전체 검사 생략이나 sync 완화를 정당화하지 않는다. 생성의 중복 직렬화와 owner 복사·정렬 정지 시간은 별도 후속 작업이며 이번에는 수정하지 않았다. 현재 snapshot 복구가 WAL-only 복구보다 빨라졌다고 주장하지 않는다.

## 테스트와 원본 보존

- deterministic 차등 입력 **6,325개**의 허용/거절과 성공 값을 기존 strictjson 기반 방식과 비교해 일치했다. leaf 스키마 제약·최대 값 경계·버퍼 소유권 검사를 추가했다.
- 차등 fuzz **30초 / 1,582,336회**, 기존 snapshot reader fuzz **31초 / 21,455회**에서 실패를 발견하지 못했다. 모든 비정상 입력을 완전히 증명하는 것은 아니다.
- `bash scripts/ci.sh portable`: **190 tests / 18 packages**. `bash scripts/ci.sh native`: **222 tests / 18 packages**, 의도한 자식 helper skip 1개. 포맷·vet·빌드, race·실제 APFS·loopback·저장/프로세스 복구 회귀를 포함한다. GitHub CI는 실행하지 않았다.
- 기존 10분 실험 234개 파일과 단계 계측 실험 129개 파일의 크기·SHA를 다시 검증해 보존을 확인했다. 이번 실행도 모든 run의 상태 파일 hash를 재검증했다.

원본은 `var/snapshot-decoder-20261030.fq2sk1r9`에 보존한다. `before`/`after`의 바이너리·Go 빌드 정보·source archive/hash, `decoder.patch`, 공통 `probe.go`, `compare.py`, `analyze.py`, 환경·원본 provenance, 10개 run/출력/검증, fuzz와 전체 CI 원본을 포함한다. 각 source archive는 기준 Git HEAD 위에 덮어쓸 변경 당시의 Go/스크립트 소스이며 공통 testdata는 기준 저장소에 있다. 커밋되지 않은 소스 차이를 바이너리 hash만으로 숨기지 않도록 두 source hash 목록을 함께 남겼다.

실행한 측정 명령은 `python3 var/snapshot-decoder-20261030.fq2sk1r9/compare.py`, 재집계는 `python3 var/snapshot-decoder-20261030.fq2sk1r9/analyze.py`다. 공통 probe는 변경 전후에 `scripts/go build -o <variant>/probe <실험 경로>/probe.go`로 각각 빌드했다. 재측정 시 새 고유 폴더에 공통 harness·provenance와 각각 검증한 바이너리를 준비한다. compare.py는 기존 results 재사용을 거절하며 환경과 양쪽 바이너리 SHA를 기록한다. 이후 소스/장비를 바꾸면 source archive·환경·HEAD 메타데이터도 함께 갱신해야 한다.

실제 전원 장애 검증은 보류 상태다. 원본 업로드·commit·master 병합·push는 수행하지 않았다.

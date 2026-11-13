# 최신 스냅샷 읽기 CPU·할당 분석

2026-11-13, master `27b4621`의 생산 코드와 최근 [이후 명령 복구 실험](snapshot-tail-recovery.md)의 동일한 active/history 각 1만 명령 최종 상태에서 CPU 각 20회·allocation 각 5회, 총 **50개 새 worker**를 수집했다. 모두 정확한 snapshot seq=10,000·tail=0·fallback 부재와 전체 최종 상태 bytes 일치를 확인했다. 생산 코드와 기존 Go profile probe는 변경하지 않았다.

**다음 첫 실험 후보는 디코더 읽기 버퍼 크기와 pread 호출 수 대조다.** 할당 후보는 배열 증가와 canonical 검증용 Marshal 반환 bytes 재사용이다. 프로파일 근거이며 아직 최적화나 개선 폭을 검증한 결과는 아니다.

[실행·해석·재집계](../snapshot-read-profiling.md), [JSON](20261113-snapshot-read-profile.json), [원본·빌드·도출 수치](20261113-snapshot-read-profile-artifacts.json)를 함께 읽는다. 실제 OS·전원 장애는 **미완료·보류**다.

## 조건과 자료

- 생산 기준 revision `27b4621d36e70ac5aebc3fcd2c46573fbb50312b`, Python 도구/테스트·문서가 추가된 dirty 작업 트리다. source 247개 archive와 binary를 동결했고 현재 source·archive member SHA를 확인했다. binary SHA는 `13a0c13a4fa407c80f34415656d269a917624da18d4e1180d870bd5ab7259b71`, archive SHA는 `c59c9394e72fc0a645576c576ca45161f656556da56730bffe126446c1c59d79`다.
- 입력 `var/snapshot-tail-full-20261113`의 원본 봉인 314개 파일/30회 검증을 다시 확인하고, 두 최종 snapshot·전체 WAL·Genesis·lock만 사본에 포함했다. 모든 반복에서 같은 runId/초기 자금/최종 bytes다. 입력 측정과 현재 생산 source 집합/내용이 같다. active는 주문 9,995/활성 9,992/체결 1건, history는 주문 4,999/활성 0/체결 2,499건이고 최초 결과는 각각 10,000개다.
- Apple M3 Pro·11 CPU·18GiB, macOS 15.8 arm64/APFS·Go 1.27.3/cgo=1, GOMAXPROCS=11/GOGC=100/GOMEMLIMIT=2GiB다. 최종 CI가 끝난 뒤 별도로 수집했다. CPU 유휴 gate/cache flush 없는 비격리 호스트·warm-cache이며 결과를 선택적으로 제외하지 않았다.
- CPU와 allocation은 별도 새 프로세스다. CPU의 기본 period=10ms·기본 메모리 sample=512KiB, allocation의 sample=64KiB와 전후 두 GC/baseline profile 차이를 확인했다. 조회/최종 Snapshot bytes 검증·종료는 profile 밖이다. 정확한 API TotalAlloc counter 차이도 별도로 남겼다.
- [과거 190 worker 분석](snapshot-profiling.md)은 WAL/직렬화 최적화 이전 코드와 다른 단순 fixture다. 이번 수치와 직접 전후 개선율을 계산하지 않는다. 최근 비계측 tail 비용과도 profile CPU/할당을 wall 중앙값으로 혼합하지 않는다.

## 경로별 결과

CPU는 표본 합계/20회인 ms/호출, allocation은 baseline 차이 합계/5회의 MiB/호출 추정치다. focus는 중첩되므로 합하지 않는다. read 안에 decode가 있고 모든 경로가 Open 아래에 있다. 부모/자식의 cumulative도 합산하지 않는다.

| 상태 | 계측 | Open 전체 | snapshot 읽기 | 그 안의 decode | restore | confirm | WAL |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| active | CPU ms/호출 | 324.500 | 147.500 | 144.500 | 5.500 | 37.500 | 32.000 |
| history | CPU ms/호출 | 277.500 | 126.500 | 121.500 | 3.500 | 31.000 | 25.000 |
| active | 추정 할당 MiB/호출 | 116.002 | 42.652 | 42.636 | 15.573 | 14.829 | 39.976 |
| history | 추정 할당 MiB/호출 | 98.588 | 33.268 | 33.221 | 14.033 | 11.734 | 36.555 |

전체 CPU 표본은 active 649개·history 555개다. 전역 CPU에는 background GC/런타임·profile 관리/호출 경계의 부수 표본이 남을 수 있다. 여러 고루틴의 CPU 합은 wall과 다르다. CPU profile은 I/O 대기 ms를 직접 측정하지 않는다. [Go 진단 지침](https://go.dev/doc/diagnostics)과 [pprof](https://pkg.go.dev/runtime/pprof)의 의미를 따른다.

allocation 5회에서 정확한 API TotalAlloc 증가 중앙값은 active 114.279MiB[114.249~114.292], history 97.290MiB[97.285~97.314]다. 전체 추정 할당과 다른 것은 sample/프로파일 직렬화·런타임 부수 비용을 포함한 측정 범위 차이이며 유지 heap/RSS·누수로 해석하지 않는다.

## 읽기 내부 병목

아래 비중의 분모는 **snapshot 읽기 focus**다. whole Open 비중과 구분한다. 예를 들어 active rawsyscalln 68.81%는 읽기 147.5ms/호출 중 101.5ms의 표본이며 전체 Open CPU의 68.81%라는 뜻이 아니다.

| 관찰 | active | history | 근거/해석 |
| --- | ---: | ---: | --- |
| rawsyscalln 자체 CPU / 읽기 CPU | 68.81% | 66.40% | 읽기 focus의 flat. 대부분 bufio fill→TeeReader→SectionReader→File.ReadAt→pread 호출 stack |
| parseArray 자체 할당 / 읽기 할당 | 40.86%, 17.429MiB/호출 | 38.00%, 12.641MiB/호출 | 각 generic 인스턴스의 flat 합. 결과/주문/호가 ref/trade 배열의 append 증가 비용 |
| json.Marshal 누적 할당 / 읽기 할당 | 36.95%, 15.758MiB/호출 | 37.88%, 12.602MiB/호출 | parseValue의 canonical 재인코딩 경로. 아래 Clone/정수 할당을 포함함 |
| bytes.Clone 자체 할당 / 읽기 할당 | 24.69%, 10.532MiB/호출 | 26.27%, 8.739MiB/호출 | Marshal이 반환하는 bytes 사본. 위 Marshal에 포함되므로 더하지 않음 |

추가 `-peek`/`-traces`에서 pread가 checksum의 CopyBuffer와 decoder의 Peek→fill 두 경로에 있음을 확인했다. active read focus의 syscall 2.03s 중 부모 SectionReader 기준 1.97s가 TeeReader·0.06s가 CopyBuffer 호출 표본이었다. 이는 표본 귀속이며 실제 syscall 호출 횟수/요청 크기/디스크 대기 시간이나 저장장치 자체의 원인 확정은 아니다. 원본은 `var/snapshot-read-profile-callers-20261113`에 봉인했다.

Go 코드에서는 checksum 확인 뒤 실제 decode bytes를 다시 TeeReader로 해시한다. reader가 checksum 이후 바뀌는 경우를 거절하는 계약이며 두 검사를 지우는 후보는 제안하지 않는다. leaf는 typed Unmarshal 뒤 표준 Marshal와 bytes를 비교해 unknown/missing/duplicate/reordered/case-folded 필드·잘못된 정수/null/UTF-8/escape를 거절한다. 현재 검증 경로를 생략해 얻는 속도는 채택 근거가 아니다.

기존 stage의 CPU-profile worker 20회 wall 중앙값도 다음과 같다. 아래는 **프로파일 수집 중 wall**이며 CPU 표본/이전 비계측 비용과 다른 측정량이다. stage별 중앙값을 더하지 않는다.

| 상태 | checksum ms | decode ms | 주문 decode ms | book decode ms | trade decode ms | 최초 결과 decode ms | restore ms | replay/prefix ms | confirm ms |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| active | 4.258 | 171.779 | 52.122 | 8.044 | 0.018 | 111.133 | 11.782 | 36.855 | 44.571 |
| history | 3.779 | 144.753 | 27.264 | 0.000 | 10.855 | 106.747 | 11.772 | 31.839 | 37.685 |

두 상태 모두 1만 개를 갖는 최초 결과의 decode stage가 크다. history의 book 0.000ms는 활성 주문이 없는 상태에서 반올림한 값이고 실행 생략이나 모든 book 비용이 0이라는 뜻이 아니다. 할당 worker의 stage도 JSON에 별도로 남겼으며 두 profiler의 표본을 합치지 않았다.

## 다음 후보와 채택 조건

| 순서 | 후보 | 이번 근거 | 다음 검증 |
| --- | --- | --- | --- |
| 1 | decoder 읽기 버퍼의 크기 대조·pread 횟수 계측 | 읽기 CPU에서 pread stack의 큰 표본, decode stage 우세. 현재 32KiB 버퍼를 decoder에 한정해 64/128/256KiB와 한 변수씩 대조할 가치가 있음 | 실제 ReaderAt 요청 횟수/크기·bytes를 별도로 기록. checksum/쓰기/confirm 버퍼는 처음에는 고정. 고정 메모리 상한·16KiB leaf/깊이 한도·두 checksum·오류/bytes/복구 계약을 유지하고 효과가 없으면 미채택 |
| 2 | 결과/주문 배열의 증가·사본 할당 줄이기 | parseArray flat이 읽기 할당 약 38~41%. 배열 증가와 DTO escaping/copy 비용을 분리할 가치가 있음 | 신뢰할 수 없는 seq/count만 보고 큰 배열을 미리 만들지 않음. bounded growth·초기 용량을 한 변수씩 대조하고 손상/조기 EOF/개수 상한·메모리 최댓값/최종 상태 일치 검사 |
| 3 | canonical leaf 비교의 Marshal 반환 bytes 줄이기 | json.Marshal 누적 약 37~38%, 그 안의 Clone만 약 25~26% | 표준 Encoder와 재사용 비교 writer 등의 후보를 검토하되 순서/escaping/개행/null/정수·오류 판정을 이전 decoder와 차등 검사. canonical 대조를 제거하지 않음 |

첫 단계는 **버퍼/호출 수 대조 실험**으로 추천한다. pprof는 syscall 수나 최적 버퍼 크기를 정하지 않으므로 아직 생산 변경을 채택하지 않는다. 할당을 우선 줄이는 목표라면 2/3번을 독립 후보로 선택할 수 있다. 여러 후보를 동시에 바꾸지 않고 개선 근거를 분리한다.

후속 최적화 판정은 같은 최종 상태와 tail 0/1천/5천의 6조건에서 baseline/candidate 비계측 Open을 각 5회 비교한다. 프로파일러는 끄고 정확한 snapshot 경로·전체 canonical bytes·첫 응답/후속 체결·취소·예약 반환·WAL 불변을 유지한다. reader의 정상/손상 corpus·golden·fuzz/native 복구도 통과해야 한다. 느린 표본·무효/실패는 보존하고 HTTP p99/지속 처리량 개선을 별도 실험 없이 주장하지 않는다.

## 검증과 보존

- Go source는 변경하지 않았다. 새 Python 회귀 4개는 50회 완전성/프로파일 분리, read-only 재집계·flat/cumulative 구분, 잘못된 입력/복구/표본·profile 누락/변조의 pprof 실행 전 거절과 작은 CPU 표본 0 처리까지 확인했다.
- 최초 작은 실행은 CPU 표본 0에서 함수 상세 조회가 실패했다(`var/snapshot-read-profile-smoke-20261113`). 표본 0을 기능 smoke로만 표시하고 full에는 유효 read/decode 표본을 요구하도록 수정했다. 실패를 보존하고 `var/snapshot-read-profile-smoke-fixed-20261113`의 새 4 worker가 통과했다.
- 최종 전체 로컬 CI: portable 232개/23 packages·native race 270개/23 packages(기존 child-helper skip 1개), 각 Python 77개와 진단/저장/HTTP/복구 smoke 통과. `var/ci/portable.VPRk8j`, `var/ci/native.vEZSvd`, `var/snapshot-read-profile-ci-portable-final-20261113.log`·`var/snapshot-read-profile-ci-native-final-20261113.log`에 보존했다. 초기 CI와 targeted/fixed 로그도 삭제하지 않았다.
- `var/snapshot-read-profile-full-20261113`: 50 worker/642개 봉인 파일·약 556.76MiB. 새 `var/snapshot-read-profile-reanalysis-20261113`에서 profile 재집계 JSON/Markdown이 원본과 bytes까지 같다. 기존 입력 314개 파일·archive 247개 member/현재 source·고정 바이너리·모든 사본/최종 bytes는 `var/snapshot-read-profile-final-proof-20261113.json`으로 확인했다.
- 기존 원본을 교체/삭제하지 않았고 4GiB 묶음 상한·별도 8GiB 여유를 유지했다. 종료 후 여유 약 9.94GiB다. 위 기록은 수집·분석 완료 시점의 로컬 계측/후속 후보 근거다. 생산 최적화는 구현하지 않았으며 후속 커밋·병합·푸시와 해당 master의 원격 CI는 별도로 확인한다.

병합 전 검토에서 동결 source 247개·archive member SHA·측정 642개 파일·기존 입력/추가 caller 자료를 다시 검증했다. 코드 수정은 필요하지 않았다. 전체 Python 77개가 `var/snapshot-read-profile-review-python-20261113.log`에서 통과했고, 원본의 새 재집계 `var/snapshot-read-profile-review-reanalysis-20261113`의 JSON/Markdown이 원본과 bytes까지 같았다. `var/snapshot-read-profile-review-proof-20261113.json`에 검토 근거를 보존했다. 50회 원본과 수치는 교체하지 않았다.

cold-cache·큰 tail/새 상태 크기·HTTP 부하/최대 처리량·OS/전원 장애의 새 검증은 아니다. 실제 OS·전원 장애는 **미완료·보류**이며 v1 전체 계획은 계속 Blocked다.

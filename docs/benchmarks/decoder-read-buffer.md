# 디코더 읽기 버퍼 32/64/128/256KiB 비교

2026-11-13, 총 1만 명령을 고정한 같은 active/history 상태에서 이후 명령 0/1천/5천, 네 버퍼를 각 5회·총 **120개 새 worker**로 비교했다. 모든 실행에서 정확한 복구 경로·전체 최종 상태 bytes·후속 체결/취소/예약 반환·최초 응답 검증이 통과했다.

**읽기 요청 수는 줄었지만 전체 복구 시간의 일관된 개선은 확인하지 못했다. 생산 기본값은 32KiB로 유지한다.** 256KiB를 기본값으로 바꾸거나 처리량/HTTP 지연 개선을 주장하지 않는다.

[실행·계수 범위·보존 정책](../decoder-read-buffer.md), [전체 JSON](20261113-decoder-read-buffer.json), [원본·빌드·fixture 근거](20261113-decoder-read-buffer-artifacts.json)를 함께 읽는다. 실제 OS·전원 장애 검증은 **미완료·보류**다.

## 조건과 격리

생산 기준은 master `104896eee3fd73216cd64d3214c755b9cbc2e0ea`다. 원래 decoder와 추적 중인 생산 파일은 그대로 두고, `decode.go`의 NewReaderSize 인자 한 곳만 32/64/128/256KiB 숫자 상수로 바꾼 Go overlay 네 빌드를 동결했다. checksum/쓰기/confirm 버퍼는 32KiB, leaf 상한 16KiB·깊이·canonical/두 checksum·WAL 전체 prefix 검증·강한 sync 계약은 유지했다.

source 255개를 archive로 동결했고 실제 overlay 텍스트·mapping·바이너리를 확인했다. 테스트 overlay는 boundedSource의 읽기 요청 상한만 각 크기에 맞추며 boundedSink 쓰기 상한과 다른 판정은 유지한다. 테스트 파일은 생산 빌드에 포함되지 않는다. 바이너리 SHA는 다음과 같다.

| KiB | SHA-256 |
| ---: | --- |
| 32 | `590e02a01658610535a88405134e050ca8d1942f04298ad5279bde45b2278445` |
| 64 | `ab4c64824d06eb3a4bb617790269955b8b12d8866181c9d67977a9aff30cc82f` |
| 128 | `b86e3daceb84e1ff8b1bba4f5534e05b503aa79150e7a5e925ffe72bb8d33903` |
| 256 | `2acf2a80caec58f972f4390b33132076a157049af008d04833908f57b8a02605` |

입력은 `var/snapshot-tail-full-20261113`의 동일한 Genesis/runId/전체 WAL과 snapshot seq 10,000/9,000/5,000이다. active 최종 주문/활성/체결은 9,995/9,992/1건, history는 4,999/0/2,499건이며 각각 최초 결과 10,000개를 보존한다. 현재 생산 source와 입력 측정 시점 source 집합/내용은 같다. 오래된 비용 수치를 baseline으로 쓰지 않고 이번 32KiB와만 비교했다.

Apple M3 Pro·11 CPU·18GiB, macOS 15.8 arm64/APFS·Go 1.27.3/cgo=1, GOMAXPROCS=11/GOGC=100/GOMEMLIMIT=2GiB다. 전체 CI·네 overlay 계약 회귀·fuzz가 끝난 뒤 본 비교를 실행했다. worker마다 새 사본/프로세스를 쓰고 조건·버퍼 순서를 회전/교대했다. profile/CPU 유휴 gate/cache flush 없는 비격리 개발 호스트·warm-cache 관찰이다.

primary 시간·메모리는 관찰자 없는 공개 Open이다. 조회/중복 재시도·최종 bytes 검증·종료 뒤 동일한 선택 prefix snapshot을 별도 Read 두 번으로 검사했다. 첫 경로는 File.ReadAt observer, 두 번째는 직접 syscall.Pread adapter다. 아래 IO 수는 그 별도 진단의 값이며 Open 자체의 커널 trace/OS 내부 재시작 횟수가 아니다.

## 전체 복구 시간

각 5회 Open 중앙값, ms다. 모든 최소/최대·GC 뒤 heap/누적 RSS·stage와 5개 대응 변화율은 JSON에 보존했다. 조건마다 작업 내용이 다르므로 여섯 조건을 평균내 하나의 승자로 만들지 않는다.

| 상태 | 이후 명령 | 32KiB | 64KiB | 128KiB | 256KiB |
| --- | ---: | ---: | ---: | ---: | ---: |
| active | 0 | 311.285 | 303.591 | 319.856 | 324.225 |
| active | 1,000 | 309.094 | 288.647 | 283.967 | 306.434 |
| active | 5,000 | 197.454 | 201.877 | 201.279 | 202.561 |
| history | 0 | 261.562 | 279.376 | 264.406 | 269.640 |
| history | 1,000 | 271.201 | 250.402 | 253.640 | 249.480 |
| history | 5,000 | 177.559 | 173.026 | 176.649 | 175.712 |

32 대비 중앙값 변화는 다음과 같다. 음수는 짧아진 관찰값이며 코드 변경의 인과적 개선 폭으로 확정하지 않는다.

| 상태 | 이후 명령 | 64KiB 변화 | 128KiB 변화 | 256KiB 변화 |
| --- | ---: | ---: | ---: | ---: |
| active | 0 | -2.47% | +2.75% | +4.16% |
| active | 1,000 | -6.62% | -8.13% | -0.86% |
| active | 5,000 | +2.24% | +1.94% | +2.59% |
| history | 0 | +6.81% | +1.09% | +3.09% |
| history | 1,000 | -7.67% | -6.48% | -8.01% |
| history | 5,000 | -2.55% | -0.51% | -1.04% |

64KiB는 6조건 중 4개, 128/256KiB는 각각 3개에서 중앙값이 낮았다. 여섯 조건 모두 개선한 크기는 없다. 256KiB의 history tail 0은 5쌍 모두 32보다 느렸고 active tail 0은 1쌍만 빨랐다. 64KiB의 active tail 0/history tail 5천은 각각 4/5쌍이 빨랐지만 다른 조건까지 일관되게 좋아지지는 않았다.

모든 느린 표본도 포함했다. 예를 들어 active tail 0의 256KiB는 305.513~474.950ms(32는 304.331~356.077), history tail 0의 256은 263.256~464.923ms(32는 254.729~320.521)였다. history tail 1천에서는 64의 최대 417.173ms·256의 최대 441.444ms도 남겼다. 원인을 버퍼/GC/디스크/다른 호스트 부하 중 하나로 확정하지 않는다.

읽기 전체 load 중앙값도 요청 수 감소에 비례해 줄지 않았다. active tail 0은 32→256에서 184.175→187.284ms, active tail 5천은 88.119→95.910ms, history tail 0은 151.780→155.856ms였다. 이전 CPU profile의 pread stack 비중만으로 읽기 호출 수 감소를 동일 비율의 시간 절감으로 환산할 수 없음을 보여 준다. 저장장치가 병목이 아니라는 증명도 아니다.

## 요청 수와 할당

표의 decode ReadAt 횟수는 각 조건의 5회 모두 같았다. checksum 횟수는 같은 cut의 네 버퍼에서 같았고, header/digest는 각 1회다. File.ReadAt 진단과 직접 Pread 진단의 문서와 요청/bytes/histogram이 모두 일치했다. 전체 120회에서 직접 Pread의 EINTR·short read는 0이었다.

| 상태 | 이후 명령 | checksum 요청 | decode 32KiB | 64KiB | 128KiB | 256KiB |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| active | 0 | 296 | 296 | 148 | 74 | 37 |
| active | 1,000 | 266 | 266 | 133 | 67 | 34 |
| active | 5,000 | 148 | 148 | 74 | 37 | 19 |
| history | 0 | 261 | 261 | 131 | 66 | 33 |
| history | 1,000 | 235 | 235 | 118 | 59 | 30 |
| history | 5,000 | 131 | 131 | 66 | 33 | 17 |

active tail 0에서 decode는 296→37회(-87.5%), 전체 진단 Pread는 594→335회(-43.60%)였다. checksum의 296회가 고정돼 전체 요청이 1/8로 줄어드는 것은 아니다. history tail 0도 전체 Pread 524→296회였다. 두 번의 checksum/읽기 bytes는 생략하지 않았다. payload P에 대해 Read가 받은 bytes 합 `2P+72`, checksum/decode 요청 수의 ceil 식을 모든 실행에서 확인했다. 준비용 header/Stat/hash와 Open의 WAL·confirm 읽기는 이 계수 범위 밖이다.

Open 총 할당 중앙값은 다음과 같다. 유지 heap/RSS와 구분한다.

| 상태 | 이후 명령 | 32KiB MiB | 64KiB | 128KiB | 256KiB |
| --- | ---: | ---: | ---: | ---: | ---: |
| active | 0 | 114.235 | 114.255 | 114.327 | 114.427 |
| active | 1,000 | 106.234 | 106.250 | 106.291 | 106.433 |
| active | 5,000 | 82.445 | 82.480 | 82.539 | 82.656 |
| history | 0 | 97.282 | 97.307 | 97.377 | 97.496 |
| history | 1,000 | 91.301 | 91.316 | 91.397 | 91.528 |
| history | 5,000 | 71.897 | 71.938 | 71.991 | 72.120 |

256의 총 할당 중앙값은 32 대비 약 0.192~0.226MiB 증가했다. 더 큰 고정 reader 버퍼와 함께 관찰된 증가이며 상태를 덜 복구해 줄인 비용은 아니다. 모든 조건의 완전한 상태와 최초 결과는 같았다.

## 복구 계약과 판단

120회 모두 snapshot 순번 10,000/9,000/5,000·재생 0/1천/5천·fallback 부재, 최종 canonical bytes가 일치했다. 상태 유형별 60개 최종 SHA가 같았고 WAL/Genesis/선택 prefix snapshot/lock도 바뀌지 않았다. 최초 응답 960건·같은 ID 재시도 240회, 이전 snapshot의 OPEN 주문 후속 체결/취소·예약금 반환/가격과 현재 주문 상태를 검사했다. 전체 1만 결과 캐시도 bytes 비교에 포함된다.

기준은 같은 엔진으로 순차 실행한 상태와 별도 정수 잔고/거래 기대값이다. 매칭 엔진 전체의 독립 구현과 대조한 증명은 아니다. CPU/할당 profile·새 HTTP 부하·프로세스 강제 종료·실제 전원 장애를 이번 측정에 섞지 않았다.

**더 큰 버퍼는 이번에 채택하지 않는다.** 요청 수 감소는 확인했지만 시간은 혼합됐고 할당은 증가했다. 32 기본값을 유지하며, 다음 후보를 고르면 [읽기 profile](snapshot-read-profiling.md)의 결과·주문 배열 증가 할당이나 canonical Marshal 반환 사본을 독립적으로 줄이는 편이 타당하다. 여러 변경을 섞거나 checksum/검증·sync를 제거해 시간을 줄이지 않는다. 더 큰 버퍼의 다른 장비/cold-cache 효과는 아직 미확정이다.

## 검증·원본 보존

- Go 새 회귀 3개는 단계별 요청 histogram·네 크기 양자화, Pread EINTR/부분 읽기/오류 전파와 실제 APFS prefix Read·문서/원본 불변을 검사했다. native/portable CI는 필요한 새 대표 테스트가 누락되면 실패한다. Python 6개는 120회 완전성/교대, 허용된 두 overlay 치환, 잘못된 호출/bytes/metadata 거절, 성공 사본 hash receipt/소유권·변조/symlink/예상 밖 파일 보존을 확인한다.
- 최초 Python 테스트의 손계산 총 호출 기대값이 1회 높아 실패했다(`var/read-buffer-python-20261113.log`). header/checksum/digest/decode 합으로 46/35/30/27로 바로잡았고 fixed/overlay 로그에서 통과했다. 원본 로그를 보존했다.
- 최초 64KiB 계약 회귀는 boundedSource의 고정 32KiB read 상한 때문에 실패했다(`var/read-buffer-overlay-contracts-20261113.log`). 생산 decoder 변경이나 거절 판정 삭제 없이 실험용 read 상한만 조건에 맞춘 뒤 `var/read-buffer-overlay-contracts-verified-20261113.log`에서 네 크기의 snapshot/runtime 전체 race 회귀가 통과했다. 쓰기/leaf 상한은 그대로다.
- reader fuzz는 32KiB 120,582회, 256KiB 473,957회(각 30초 설정)가 통과했다. `var/read-buffer-fuzz-32-20261113.log`·`var/read-buffer-fuzz-256-20261113.log`에 보존했다. 해당 생산 decoder 변형은 최종 빌드와 같으며 이후 바뀐 것은 실험용 테스트 상한과 조정기/회귀다. 유한 fuzz를 전체 입력의 증명으로 확대하지 않는다.
- 최종 로컬 CI: portable 234개/23 packages·native race 273개/23 packages(기존 child-helper skip 1개), 각 Python 83개·진단/저장/거래/HTTP/복구 smoke 통과. `var/ci/portable.QpYpDj`, `var/ci/native.yl6is2`, `var/read-buffer-ci-portable-final-20261113.log`·`var/read-buffer-ci-native-final-20261113.log`에 보존했다.
- `var/read-buffer-builds-verified-20261113`의 네 빌드와 `var/read-buffer-smoke-verified-20261113`의 실제 24개 기능 worker를 확인했다. 이전 예비 빌드/smoke/CI/실패 자료도 남겼다. 작은 자료의 비용은 본 결과에 포함하지 않았다.
- `var/read-buffer-full-20261113`: 120개 결과·757개 봉인 파일, 논리 크기 약 69.30MiB다. 새 성공 임시 사본 120개만 hash/size/owner receipt를 저장한 뒤 정리했고, 공통 fixture/고정 빌드/실패·이전 원본은 삭제하지 않았다. 별도 여유 약 8.70GiB와 4GiB 상한을 지켰다.
- `var/read-buffer-reanalysis-20261113`의 JSON/Markdown은 원본과 bytes까지 같다. source archive 255개 member/현재 source·허용 overlay/바이너리·기존 입력 314개 파일·모든 IO 결과/receipt를 `var/read-buffer-final-proof-20261113.json`에서 확인했다. `var/read-buffer-reconstruction-20261113`에서는 두 상태×32/256×tail 0/5천의 8개 사본을 공통 자료로 새로 만들어 다시 실행했고 모두 통과했다. 이는 기능 재구성 증거이며 본 120회 시간 표에 합치지 않았다.

병합 전 검토에서 코드 수정 없이 동결 source 255개·archive member SHA·757개 파일·120개 결과·기존 입력/재구성 자료를 재검증했다. Python 83개와 tools/statescale·tools/civerify의 race 검사가 다시 통과했다(`var/read-buffer-review-python-20261113.log`, `var/read-buffer-review-go-20261113.log`). `var/read-buffer-review-reanalysis-20261113`의 새 JSON/Markdown은 원본과 bytes까지 같다. 검토 근거는 `var/read-buffer-review-proof-20261113.json`에 보존했고 측정 원본과 수치는 바꾸지 않았다.

위 기록은 실험 도구 구현·수집/분석 시점의 로컬 근거다. 후속 커밋·병합·푸시와 해당 master의 원격 CI는 별도로 확인한다. 실제 OS·전원 장애는 **미완료·보류**, v1 전체 계획은 계속 Blocked다.

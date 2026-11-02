# 동일 상태의 스냅샷 생성·복구 단계 측정

2026-10-30, `codex/snapshot-stage-metrics`의 계측 변경을 사용했다. 기준 HEAD는 `3b5a6f5`이고 작업 트리가 수정된 빌드다. 단계별 관측을 추가했으며 최적화 성과를 주장하지 않는다. [원본 재집계 JSON](20261030-snapshot-stages.json), [보존 파일 manifest 요약](20261030-snapshot-stage-artifacts.json), [계측 계약](../design-docs/benchmark-v1.md#스냅샷-생성복구-단계-계측)을 함께 읽는다.

## 결론

같은 최종 상태에서 `runtime.Open` 중앙값은 WAL 전체 재생 **1,133.782ms**, snapshot 복구 **3,933.784ms**였다. snapshot 경로가 약 **3.47배** 오래 걸렸다. 가장 큰 추가 비용은 snapshot JSON 해석 **2,231.543ms**, canonical bytes 재확인 **514.487ms**다. snapshot 복구도 WAL 최초 검사와 전체 prefix 대조를 수행하므로 WAL 비용이 사라지지 않는다.

새 snapshot 생성은 중앙값 **602.633ms**였다. payload 길이를 알아내기 위한 직렬화 **239.373ms**, 실제 직렬화·checksum·파일 쓰기 **307.196ms**가 합쳐서 전체의 약 **90.7%**다. owner 사본 생성 전체는 **43.061ms**여서 응답 지연 관점에서 따로 다뤄야 한다. 이 비율은 각 단계 중앙값/전체 중앙값의 근사이며 부모·자식 시간을 합친 값이 아니다.

## 조건과 동일성

[앞선 10분 비교](soak-10m-snapshot-comparison.md)의 snapshot 조건 최종 상태 하나를 사용했다. 서로 다른 두 실험의 최종 상태를 비교하지 않았다.

| 항목 | 고정 값 |
| --- | --- |
| 순번·중복 결과 | 51,385 |
| 계정·주문·체결 | 4 / 45,280 / 12,117 |
| 매수·매도 가격 단계 | 7 / 5 |
| WAL / snapshot bytes | 13,626,452 / 52,307,324 |
| snapshot SHA-256 | `6d836cda051929a34551776a6892aa5bda478058ea16bd23a75b27b41a355abe` |
| WAL SHA-256 | `ca5b905e7fcac7012e04a2961f65af755a9ef44bc71c0bbedf535f2f3abb8e16` |
| 실행 환경 | Apple M3 Pro, 11 CPU, 18 GiB RAM, macOS 15.8 arm64/APFS, Go 1.27.3, cgo=1 |
| Go 제어 | GOMAXPROCS=11, GOGC=100, GOMEMLIMIT=off, GODEBUG 미설정 |
| 반복 | 조건별 5회, 매번 새 프로세스·새 run 사본, 홀수 WAL→snapshot / 짝수 snapshot→WAL |

각 run에 동일 `.lock`, Genesis와 WAL을 복사하고 snapshot 조건에만 최신 snapshot 한 개를 추가했다. 복사·해시는 측정 전에 끝낸다. 프로세스 하나가 `runtime.Open`으로 복구한 뒤 `runtime.Snapshot`을 한 번 수행하고 정상 종료한다. 새 파일 생성 시간은 WAL 조건의 첫 publication으로 잰다. snapshot 조건의 publication은 기존 파일 재확인으로 별도 분류했다.

모든 실행에서 복구 순번·재생 수·건너뛴 후보 없음·publication 성공을 확인했다. WAL 조건에서는 새 snapshot을 원본과 비교하고, snapshot 조건에서는 owner가 다시 캡처한 전체 상태의 canonical bytes를 기존 파일과 비교하도록 기존 Publish 경로를 실행했다. **10회 모두 전체 snapshot bytes가 원본과 같고 WAL·Genesis·lock bytes도 변하지 않았다.** 주문·잔고·호가·체결·최초 결과를 포함한 전체 상태의 검증이며, 이번 실행은 HTTP를 거치지 않는다. 기존 10분 실험의 234개 원본 파일도 전부 다시 해시 검증했다.

## 반복 결과

시간 단위는 ms, 괄호는 5회 최소~최대다. `Open`은 runtime 복구 시간이며 새 HTTP 서버의 ready까지 걸리는 시간과 다르다.

| 항목 | WAL 전체 재생 | snapshot 복구 |
| --- | ---: | ---: |
| Open 전체 | 1,133.782 (1,128.861~1,180.155) | 3,933.784 (3,902.181~4,031.621) |
| WAL open·최초 검사 | 525.654 (508.576~527.524) | 504.188 (501.876~530.540) |
| snapshot load | 해당 없음 | 2,252.933 (2,235.985~2,326.303) |
| 엔진 rebuild·의미 검사 | Genesis 신규 엔진 | 89.496 (84.079~96.790) |
| WAL 대조/적용·최종 불변식 | 613.472 (597.773~643.591) | 563.200 (553.601~568.605) |
| snapshot canonical 재확인·sync | 해당 없음 | 514.487 (502.384~531.496) |
| WAL 복구 sync | 7.631 (3.588~8.882) | 3.956 (2.040~8.972) |
| 실제 새로 적용한 명령 | 51,385 | 0 |

snapshot은 suffix 적용이 0건이어도 보존 WAL 51,385건을 검사한다. 이는 현재 복구 계약이며 이 작업에서 생략하지 않았다. 각 행의 중앙값을 합해 전체 중앙값으로 사용하면 안 된다.

snapshot load의 주요 하위 비용:

| 단계 | 중앙값 | 최소~최대 |
| --- | ---: | ---: |
| checksum·envelope | 21.354 | 20.831~23.269 |
| JSON decode 전체 | 2,231.543 | 2,215.124~2,303.003 |
| decode.results | 1,478.654 | 1,472.683~1,550.038 |
| decode.orders | 608.521 | 602.642~614.667 |
| decode.trades | 123.525 | 121.896~126.410 |
| decode.book | 17.045 | 16.561~17.872 |

results는 decode 전체의 약 66.3%다. 이 구간은 문자열 경계 파싱, strictjson 검증·DTO 복원, canonical 재직렬화 비교, 객체 할당·GC, 파일 읽기를 함께 포함한다. 파서 연산 하나만 잰 값은 아니다. Open 직후 프로세스 누적 할당량 중앙값도 WAL 약 653MB, snapshot 약 2,195MB이고 GC 누적 횟수는 102 대 165였다. 누적 할당량은 RSS나 살아 있는 heap 크기가 아니다.

## 생성 비용

새 파일을 게시한 WAL 조건의 5회 결과다. 정지된 동일 상태이고 동시 주문은 없다.

| 단계 | 중앙값 ms | 최소~최대 ms |
| --- | ---: | ---: |
| Snapshot 호출 전체 | 602.633 | 588.457~640.076 |
| owner capture 전체 | 43.061 | 40.813~43.513 |
| capture.copy | 14.417 | 12.698~15.416 |
| capture.sort | 27.921 | 27.067~29.346 |
| publisher 전체 | 559.343 | 547.615~596.527 |
| publish.measure | 239.373 | 237.454~249.970 |
| publish.encodeWrite | 307.196 | 296.826~331.374 |
| 새 파일 strict sync | 7.199 | 3.882~8.487 |
| directory strict sync | 3.883 | 2.147~7.847 |

기존 동일 파일을 다시 확인하는 snapshot 조건은 호출 전체 544.401ms (528.031~548.427), payload 길이 산출 233.315ms, bytes 비교 259.281ms였다. 새 파일 쓰기·rename은 실행하지 않았다. 이 값을 새 파일 생성 성능과 섞지 않는다.

## 별도 진단과 해석 한계

반복 측정과 분리한 snapshot 복구 CPU profile 한 개를 보존했다. `decoder.value`, strictjson 검증과 메모리 관리 경로가 관측됐지만 `syscall` stack 비중도 크게 나타났다. 이 sample 비중을 디스크 대기 시간으로 해석하지 않았다. 별도 ReadAt wrapper로 실제 읽기 호출 경계의 wall time을 직접 3회 측정했다.

- 세 번 모두 3,196회 / 104,614,616 bytes. checksum과 실제 decode의 두 번 읽기다.
- 전체 snapshot Read는 2,292.834 / 2,360.188 / 2,261.650ms, 파일 ReadAt 누계는 44.090 / 48.530 / 24.089ms였다.
- 이 조건에서 직접 파일 읽기는 전체의 약 1.1~2.1%다. CPU profile의 syscall sample 비중과 차이가 있으므로 sample만으로 파일 읽기가 주원인이라고 결론 내리지 않는다. 프로파일·ReadAt wrapper 실행은 앞의 5회 통계에 넣지 않았다.

캐시를 강제로 비우지 않았으며 사본 복사·해시가 OS 캐시를 데운다. cold-cache나 다른 디스크의 결론으로 확대할 수 없다. 백그라운드 OS 작업·계측 오버헤드를 제거하지 않았고 5회는 통계적 유의성을 보장하지 않는다. 같은 상태 크기 하나의 진단이며 상태 증가의 복잡도를 측정한 것은 아니다. 앞선 10분 결과와 입력·경로·동시 부하가 달라 이번 수치를 성능 개선이라고 비교하지 않는다. owner capture 43ms는 p99 20ms 목표에 위험한 정지 구간이지만, 실제 주문 지연 영향은 부하 실험으로 확인해야 한다.

## 다음 개선 순서

1. **복구 디코더부터 작은 후보를 만든다.** `decode.results`와 `decode.orders`의 반복 파싱·검증·재직렬화 및 임시 객체 생성을 프로파일로 더 좁힌다. 중복/미지 필드·필수 필드·정수·크기·canonical bytes 검사와 실패/fallback 계약을 유지한다. 동일 상태 5회 복구와 byte 일치를 다시 비교한다.
2. **생성의 두 번 직렬화를 줄일 수 있는 설계를 검토한다.** 길이 산출과 파일 쓰기가 각각 전체 문서를 직렬화한다. 메모리 증가, 최대 크기 제한, checksum, 부분 실패와 기존 포맷 호환성을 함께 평가한다. sync 제거·완화는 이 결과에서 도출되지 않는다.
3. **owner 정지 시간을 별도 개선 목표로 둔다.** 복사와 정렬은 worker의 직렬화와 다른 문제다. 과거 [정렬 이동 후보](p7-snapshot-sorting.md)는 반복 기준 미달로 미채택했다. 단순히 재채택하지 않고, 불변 상태 표현·사본 생성 방식을 검토한 뒤 snapshot을 켠 동일 부하에서 p99를 확인한다.

최적화는 이번 작업에서 수행하지 않았다. 실제 전원 장애 검증은 계속 보류한다.

## 재현 자료와 검증

원본은 `var/snapshot-stages-20261030.ye3p3gdp`에 보존한다. `probe.go`/바이너리, `repeat.py`, `analyze.py`, provenance·환경, 10개 run/출력/검증, 읽기 진단, CPU profile, 실행 소스 archive와 로컬 CI 원본을 포함한다. 초기 잠금 파일 복사 누락으로 실패한 준비 실행은 `var/snapshot-stages-20261030.087x5dxs`에 남겼으며 성공한 반복 통계에 포함하지 않았다. 원본·바이너리는 Git에 넣거나 업로드하지 않는다.

실제 실행 명령은 `scripts/go build -o var/snapshot-stages-20261030.ye3p3gdp/probe var/snapshot-stages-20261030.ye3p3gdp/probe.go`에 해당하는 동일 소스 빌드와 `python3 var/snapshot-stages-20261030.ye3p3gdp/repeat.py`, 이후 `python3 var/snapshot-stages-20261030.ye3p3gdp/analyze.py`다. 최초 디렉터리에서 빌드한 바이너리를 수정 없이 복사했으며 SHA는 manifest로 확인한다. repeat.py는 기존 results 재사용을 거절한다. 재측정 시 새 고유 디렉터리에 probe.go/repeat.py/provenance.json을 복사하고 빌드·환경/HEAD 메타데이터를 현재 조건에 맞게 갱신한 뒤 실행한다. 파일 복사 시간과 원본 해시는 측정 구간 밖이다.

`bash scripts/ci.sh portable`: **187 tests / 18 packages**, `bash scripts/ci.sh native`: **219 tests / 18 packages**, 의도한 자식 helper skip 1개. 실제 APFS 저장·복구, loopback/자식 프로세스, race·포맷·vet·빌드를 포함한다. 앞서 관련 5개 패키지 race 검사도 통과했다. 계측의 중첩/완료 시점·동시 조회·보고서 분리, byte 보존, 재게시와 파일 sync 실패, checksum 실패 이후 decode 미실행, CLI ready 단계 출력을 검증했다.

측정 후 리뷰에서 WAL/state 재생 실패 시 실제로 수행하지 않은 최종 불변식 검사를 단계 건수에 포함하지 않도록 보완하고 최종 전체 CI를 다시 통과했다. 성공 복구의 처리·저장·검증 순서는 유지한다. 5회 통계의 소스는 `source-code.tar.gz`, 최종 코드는 `source-code-final.tar.gz`, 차이는 `post-measurement-error-metrics.patch`로 구분해 보존했다.

기존 ignored 실험 폴더의 분석용 Go 파일이 전체 CI의 `./...`에 포함되는 문제가 있어 원본을 바꾸지 않고 `var/go.mod`로 로컬 실험 자료의 모듈 경계를 만들었다. 처음 sandbox에서 loopback 바인딩이 거절된 검사는 실행 권한을 부여한 테스트로 다시 검증했다. 위 숫자는 최종 성공 실행만 가리키며 GitHub CI 실행·commit·master 병합·push를 의미하지 않는다.

# 스냅샷 이후 명령 복구·비용 비교

2026-11-13, **총 1만 고유 명령을 고정하고 이후 명령 0·1천·5천개를 active/history 각각 5회, 총 30개 새 프로세스에서 확인했다. 모두 최종 상태 전체 bytes가 일치했다.** 양수 tail에서 snapshot 이전 주문의 후속 체결·취소·예약금 반환과 최초 OPEN 응답 복구를 확인했다. 생산 코드는 변경하지 않았다.

[실행·검증 범위](../snapshot-tail-recovery.md), [JSON 결과](20261113-snapshot-tail-recovery.json), [원본·동결 빌드·fixture SHA](20261113-snapshot-tail-artifacts.json)를 함께 읽는다. 실제 OS·전원 장애 검증은 **미완료·보류**다.

## 조건과 실행한 빌드

생산 기준은 snapshot 직렬화 최적화를 포함한 master `ef8cc8f1c53426c408e72ebcba218c445637697c`다. 해당 HEAD와 `internal`·`cmd`·Go 설정이 같고, 실험 도구/테스트를 추가한 dirty 작업 트리를 245개 source 파일 archive로 동결했다. binary SHA는 `2f6c96e682ebc86f53c487acc82fb6c22a6a77604b40c0c9ded627ac6a89aec2`, archive SHA는 `41a1a1c92060c5f4a4881d4cff12301656ef0e0ba2d42625e2a0be0f33d1f3a9`다.

Apple M3 Pro·11 CPU·18GiB, macOS 15.8 arm64/APFS·Go 1.27.3/cgo=1, GOMAXPROCS=11/GOGC=100/GOMEMLIMIT=2GiB다. 전체 CI가 끝난 뒤 측정했다. profile/CPU 유휴 gate/cache flush는 사용하지 않았다. 사본/해시 뒤 warm-cache·비격리 개발 호스트 관찰이며 느린 표본도 모두 포함했다.

상태 유형마다 동일한 초기 자금/runId/전체 WAL/최종 상태를 사용하고 선택 snapshot 순번만 10,000/9,000/5,000으로 바꿨다. 양수 tail의 마지막 여섯 명령은 snapshot 이전 두 주문의 체결/취소와 체결된 주문·이미 취소된 주문·없는 주문의 취소 결과를 포함한다. history는 prefix 안의 부분 체결 주문 취소도 suffix에서 이어진다. tail 0은 이미 모든 거래가 반영된 snapshot이다.

active 최종 주문/활성/체결/취소 주문은 9,995/9,992/1/2·체결 1건, history는 4,999/0/2,499/2,500·체결 2,499건이다. 최초 결과 캐시는 각각 10,000개다. 이전 단순 상태 비용 실험과 명령열이 다르므로 과거 측정을 전후 baseline으로 사용하지 않는다.

## 실제 비용

총 10000명령, 조건별 5회 중앙값 [최소~최대], 30 worker 상태 일치.

| 상태 | 이후 명령 | snapshot 순번 | snapshot MiB | Open ms | 할당 MiB | GC 뒤 heap 증가 MiB | 누적 peak RSS MiB |
| --- | ---: | ---: | ---: | --- | --- | --- | --- |
| active | 0 | 10000 | 9.235 | 297.963 [295.702~339.626] | 114.217 [114.188~114.247] | 10.618 [10.613~10.619] | 46.078 [45.562~47.141] |
| active | 1000 | 9000 | 8.312 | 281.546 [275.381~311.251] | 106.211 [106.187~106.218] | 10.565 [10.559~10.566] | 43.625 [42.062~44.266] |
| active | 5000 | 5000 | 4.616 | 194.911 [189.551~205.626] | 82.454 [82.412~82.463] | 10.357 [10.356~10.362] | 36.375 [35.609~36.766] |
| history | 0 | 10000 | 8.135 | 263.140 [259.084~412.186] | 97.285 [97.263~97.305] | 9.327 [9.321~9.330] | 41.094 [39.375~45.125] |
| history | 1000 | 9000 | 7.322 | 256.811 [234.528~304.396] | 91.313 [91.293~91.333] | 9.350 [9.346~9.352] | 38.031 [37.688~38.984] |
| history | 5000 | 5000 | 4.066 | 178.166 [162.139~179.588] | 71.907 [71.881~71.913] | 9.208 [9.197~9.209] | 32.938 [31.891~34.188] |

| 상태 | 이후 명령 | load ms | restore ms | replay/prefix ms | confirm ms |
| --- | ---: | --- | --- | --- | --- |
| active | 0 | 174.061 [171.076~185.938] | 11.583 [11.412~14.841] | 36.118 [34.548~41.823] | 47.131 [43.235~62.677] |
| active | 1000 | 158.077 [156.371~176.068] | 12.655 [12.248~14.564] | 37.515 [35.433~41.083] | 40.017 [38.489~45.542] |
| active | 5000 | 89.446 [87.703~95.586] | 5.756 [5.160~6.605] | 38.945 [37.649~41.801] | 25.233 [24.530~26.549] |
| history | 0 | 152.203 [148.501~160.523] | 12.248 [11.414~12.860] | 33.839 [31.480~109.475] | 37.862 [36.086~102.708] |
| history | 1000 | 136.487 [132.508~187.081] | 11.001 [9.587~14.481] | 32.846 [30.949~36.143] | 36.074 [32.141~38.244] |
| history | 5000 | 78.761 [73.841~87.487] | 6.158 [5.714~7.668] | 34.044 [33.192~38.451] | 20.893 [20.687~22.321] |

같은 상태 유형의 전체 WAL/초기 자금/최종 상태를 고정하고 snapshot cut만 변경했다. 전체 WAL prefix 검사·strict sync는 유지한다.
조회/최초 응답/중복 재시도/최종 snapshot bytes 검증은 Open 시간 밖이다. 준비/사본/해시/강제 GC도 제외하고 자연 GC·I/O 대기는 포함한다.
새 프로세스·warm-cache·비격리 호스트이며 느린 표본도 포함한다. 실제 전원 장애 검증은 미완료·보류다.

## 해석

이후 명령이 많으면 반드시 더 오래 복구될 것이라는 가정과 달리, 이번 세 조건에서는 **작은 prefix snapshot + 5천 명령 재생이 tail 0보다 짧았다.** active Open 중앙값은 297.963→281.546→194.911ms, history는 263.140→256.811→178.166ms다. 5천 조건은 0 대비 각각 34.59%/32.29% 낮았고 총 할당은 27.81%/26.09% 적었다. 이는 코드 전후 개선율이 아니라 이번 서로 다른 cut 조건의 관찰 차이다.

모든 조건에서 전체 1만 명령 WAL을 검사한다. tail 0도 prefix 입력/순번/최초 결과 대조를 생략하지 않으며, tail이 늘 때는 prefix 대조의 일부가 suffix Apply로 바뀐다. replay/prefix 중앙값은 active 약 36.1~38.9ms, history 약 32.8~34.0ms로 크게 늘지 않았다. 반면 작은 snapshot은 load와 기존 파일 Confirm의 bytes가 줄었다. active load는 174.061→89.446ms, Confirm은 47.131→25.233ms이고 history는 152.203→78.761ms·37.862→20.893ms다. 이 단계 관측은 이번 전체 시간 감소가 snapshot 처리 비용 감소와 주로 함께 나타났음을 보여 준다. 단계별 중앙값을 더해 전체 중앙값으로 계산하지 않는다.

같은 최종 상태이므로 GC 뒤 heap 증가 중앙값은 active 약 10.36~10.62MiB, history 약 9.21~9.35MiB로 비슷하다. 적은 총 할당/누적 RSS를 최종 상태를 생략한 결과로 설명하지 않는다. RSS는 Open 직후까지의 프로세스 누적 high-water다.

범위가 겹치는 0/1천 조건의 작은 시간 차이는 반복 5회의 호스트 관찰이다. history tail 0의 최대 412.186ms(replay 109.475ms·Confirm 102.708ms), tail 1천의 최대 304.396ms도 포함했다. 이 느린 표본의 OS 원인을 확정하지 않는다. tail을 무한히 늘려도 빨라진다는 뜻이 아니며, 최적 snapshot 간격/HTTP p99/최대 지속 처리량을 결정하는 근거로 확대하지 않는다. 다음 비용 후보를 고르면 여전히 큰 snapshot load의 검증/디코딩을 더 좁혀 보는 것이 적절하나 이번에는 최적화하지 않았다.

## 거래·최초 응답 복구 근거

- 30회 모두 선택 snapshot 파일/순번·재생 수 0/1,000/5,000이 정확했고 fallback은 없었다. 상태 유형별 15개 최종 canonical snapshot SHA가 모두 준비한 순차 최종 파일과 같았다. 입력·결과 캐시 전체 10,000개도 bytes 비교에 포함된다.
- 재시도 전에 공개 Read로 회당 최초 응답 8개, 총 240개를 비교했다. 첫 두 응답은 OPEN으로 남아 있고 현재 주문은 CANCELLED다. 후속 최초 응답은 ACCEPTED/CANCELLED/CANCELLED/ALREADY_FILLED/ALREADY_CANCELLED/ORDER_NOT_FOUND였다.
- `carry-fill`은 maker 9,990 ticks에서 1 lot 체결·1 lot 취소, `carry-cancel`은 1 lot 전량 취소, bob taker는 1 lot FILLED였다. maker/taker 참조와 거래 순번/가격/수량을 확인했다.
- 각 계정의 초기 base=10,100 lots, quote=200,100,000 atoms에서 독립 산술 기대값으로 정산/예약 반환을 검사했다. active는 일반 매수/매도 각 4,996개가 남아 alice quote reserved=49,460,400, bob base reserved=4,996이고, 두 carry 주문의 예약은 모두 해제됐다. history는 두 계정 reserved=0이다.
- 최초 두 명령을 회당 2개·총 60번 재시도해 최초 응답을 그대로 받았다. as-of seq=10,000과 WAL bytes는 같았다. 선택 prefix snapshot·Genesis·lock도 바뀌지 않았다.

전체 상태 기준은 같은 엔진의 순차 실행이며 별도 잔고/가격/상태 기대값을 더했다. 거래 엔진의 완전한 독립 구현과 대조한 검증은 아니다. 새로운 HTTP 부하/프로세스 강제 종료/실제 전원 장애를 이번 캠페인에 섞지 않았다. 기존 계약과 생산 구현은 유지했다.

## 검증·보존

- `TestTailStreamAndIndependentExpectations`는 24명령의 고정 숫자 기대값·cut OPEN 상태·최종 상태와 최초 응답을 검증한다. 입력 범위/미지원 환경은 파일 생성 전에 거절하고, native는 두 상태의 실제 세 cut·소유 사본·원본 불변과 다른 최신 snapshot이 섞인 경로 거절을 확인한다. CI는 mode별 대표 tail 테스트가 빠져도 실패한다.
- Python 회귀 4개는 30회 완전성/순서, read-only 재집계, 잘못된 복구 경로·첫 응답/재시도 증거·GC/메모리·입력/출력/동결 빌드 변조·누락을 거절한다.
- 로컬 전체 CI: portable 232개/23 packages, native race 270개/23 packages(기존 child-helper skip 1개), 각각 Python 72개와 진단/거래/저장/프로세스/HTTP/복구 smoke 통과. `var/ci/portable.4zdoDM`, `var/ci/native.1bUHMI`, `var/snapshot-tail-ci-portable-20261113.log`, `var/snapshot-tail-ci-native-20261113.log`에 보존했다.
- 초기 native 검증은 체결 조회에 임의 순번 커서를 넣어 실패했다(`var/snapshot-tail-targeted-20261113.log`). 기존 계약은 실제 이전 체결 커서를 요구하므로 검증 코드를 고쳐 `var/snapshot-tail-targeted-fixed-20261113.log`에서 통과했다. 생산 거래/조회 계약은 바꾸지 않았다.
- `var/snapshot-tail-smoke-20261113`은 실제 24명령 6회/최초 응답 48건·재시도 12건 검증이며 비용 표에는 포함하지 않았다. 이후 source 정리 뒤 전체 CI와 full 동결을 수행했다.
- `var/snapshot-tail-full-20261113`의 봉인 파일 314개·약 489.91MiB를 검증했다. `var/snapshot-tail-reanalysis-20261113`에서 새 JSON/Markdown이 원본과 bytes까지 같았다. archive member 245개 SHA·측정 시점 source·동결 바이너리·WAL/세 snapshot·30개 결과를 `var/snapshot-tail-final-proof-20261113.json`으로 확인했다. 종료 후 디스크 여유는 약 10.59GiB이고 기존 원본은 삭제하지 않았다.

병합 전 검토에서 생산 코드의 새 미추적/ignore 파일도 HEAD 출처 검사에서 거절하도록 조정기를 보완했다. 실제 임시 Git 저장소에서 clean/도구만 변경/생산 파일 신규·ignore·staged·unstaged 사례를 확인하는 Python 회귀를 추가했다. 측정 이후 차이는 조정기·그 테스트 두 Python 파일뿐이고 모든 Go/생산 source는 같다. `var/snapshot-tail-review-proof-20261113.json`에서 기존 314개 파일과 archive member 245개·보고서 bytes를 다시 확인했고 30회 원본/수치는 교체하지 않았다. 수정 도구의 실제 6조건 smoke도 `var/snapshot-tail-review-smoke-20261113`에서 통과했다. 이는 기능 검증이며 본 비용 표에 합치지 않았다.

검토 수정 뒤 전체 CI를 다시 실행해 portable 232개·native race 270개(각 23 packages), 각 Python 73개와 진단 smoke가 모두 통과했다. `var/ci/portable.AtzjDk`·`var/ci/native.b5D1NF`, `var/snapshot-tail-review-ci-portable-20261113.log`·`var/snapshot-tail-review-ci-native-20261113.log`에 보존했다. 새 기능 smoke의 동결 Go 바이너리 SHA도 30회 본 측정 바이너리와 같고 현재 245개 source와 일치함을 확인했다.

위 측정과 검증은 구현/검토 시점의 로컬 결과다. 후속 커밋·병합·푸시와 해당 master 커밋의 원격 CI 결과는 별도로 확인한다. 실제 OS·전원 장애는 **미완료·보류**, v1 전체 계획은 계속 Blocked다.

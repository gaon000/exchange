# 최적화 후 스냅샷 읽기 CPU·할당 분석

2026-11-13, 배열 증가·정규형 비교 버퍼 최적화가 포함된 최신 생산 코드에서 active/history 각 1만 명령·tail=0 상태를 다시 계측했다. CPU 각20회·allocation 각5회, 총 **50개 새 worker 모두 전체 복구 상태 bytes·snapshot 선택·WAL 불변 검증을 통과**했다. 다음 첫 후보는 **정수 JSON 디코딩의 임시 문자열 할당을 줄이는 것**이다. 이번 작업은 프로파일 갱신이며 추가 생산 최적화를 적용한 결과는 아니다.

[수집·재집계 명령](../snapshot-read-profiling.md), [JSON](20261113-post-optimization-profile.json), [검증·고정 빌드·도출 수치](20261113-post-optimization-profile-artifacts.json)를 함께 읽는다. 이전 [읽기 프로파일](snapshot-read-profiling.md)과 [정규형 버퍼 변경 전 할당 프로파일](snapshot-canonical-buffer.md)의 비중을 현재 비중으로 사용하지 않는다. 실제 전원 장애는 **미완료·보류**다.

## 조건과 검증 범위

- 기준 revision은 RSS 진단 커밋 `dc7f10fe242f268f16233ee67eb4f4de698b4f06`이고 Python 입력 검증 변경이 있는 dirty 작업 트리다. 생산 코드는 master `24bc995` 및 입력 RSS candidate와 같다. Go profile probe도 변경하지 않았다. RSS 진단의 시간별 관측·추가 GC/강제 OS 반환은 실행하지 않았다. 기존 profile의 시작 전 GC와 allocation 전후 두 GC는 유지한다.
- `var/recovery-rss-full-20261113`의 128개 봉인 파일·20회 진단 검증과 candidate 생산 source를 확인한 뒤 최종 snapshot/WAL/Genesis/lock 사본을 사용했다. 동일 runId·초기 자금·최종 파일이다. active는 주문9,995/활성9,992/체결1건, history는 주문4,999/활성0/체결2,499건이며 최초 결과는 각각10,000개다. profile 종료 뒤 공개 Snapshot으로 결과 캐시·잔고·주문·체결을 포함한 전체 canonical bytes를 대조했다. 이번 profile worker에서 개별 최초 응답 재시도나 후속 거래를 새로 실행한 것은 아니다.
- Apple M3 Pro·11CPU·18GiB, macOS15.8 arm64/APFS·Go1.27.3/cgo1, GOMAXPROCS11/GOGC100/GOMEMLIMIT2GiB다. CPU period10ms/메모리 기본 sampling512KiB, allocation sampling64KiB·전후 profile 차이다. 상태 순서를 교대하고 각 worker를 새 프로세스로 실행했다. CPU·allocation을 동시에 켜지 않았다.
- warm-cache·CPU 유휴 gate 없는 비격리 개발 호스트다. 50회 수집이 끝난 뒤 전체 CI를 시작했고 느린 표본을 제외하지 않았다. 4GiB 묶음 상한·별도8GiB 여유·준비128MiB gate를 유지했다. CPU/할당 focus는 중첩하므로 합산하지 않는다. CPU는 표본 합/호출 수이며 I/O 대기 wall이나 syscall 횟수가 아니다. allocation은 표본 추정치이고 정확한 API TotalAlloc·GC 뒤 heap·RSS와 다르다.

## 경로별 결과

| 상태 | 계측 | Open 전체 | snapshot 읽기 | 그 안의 decode | restore | confirm | WAL |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| active | CPU ms/호출 | 296.000 | 135.000 | 134.500 | 5.000 | 31.500 | 32.000 |
| history | CPU ms/호출 | 254.000 | 116.000 | 109.000 | 5.000 | 30.500 | 25.500 |
| active | 추정 할당 MiB/호출 | 96.001 | 23.395 | 23.379 | 15.031 | 14.844 | 39.518 |
| history | 추정 할당 MiB/호출 | 85.937 | 19.579 | 19.547 | 14.318 | 11.988 | 37.275 |

전체 CPU 표본은 active592개·history508개다. 전역에는 runtime/GC·프로파일 부수 비용이 남을 수 있다. allocation worker의 정확한 API TotalAlloc 증가 중앙값은 active **95.915MiB[95.893~95.938]**, history **83.638MiB[83.630~83.644]**다. 표본 추정 Open 전체와 다른 범위/추정 오차를 혼동하지 않는다. 이번 profile을 과거 profile과 단순 나눠 최적화 개선율·HTTP 성능 개선으로 해석하지 않는다.

## 읽기 내부의 남은 비용

아래 분모는 snapshotRead의 추정 할당이며 flat 자체 비용이다. parseArray·parseValues는 모든 generic 인스턴스의 flat을 각각 합했다. 부모 cumulative와 합치지 않는다.

| 함수/묶음 | active MiB/호출 | active 비중 | history MiB/호출 | history 비중 |
| --- | ---: | ---: | ---: | ---: |
| parseArray 자체 | 9.140 | 39.07% | 7.589 | 38.76% |
| parseValues item 임시 객체 | 3.279 | 14.02% | 2.540 | 12.98% |
| Int.MarshalJSON 자체 | 4.688 | 20.04% | 3.826 | 19.54% |
| Int.UnmarshalJSON 자체 | 3.238 | 13.84% | 2.450 | 12.51% |
| bytes.Clone 자체 | 표본 없음 | 0.00% | 0.063 | 0.32% |

이전 Marshal 반환 사본이 현재도 약30%라는 해석은 맞지 않는다. 현재 canonical `Encoder.Encode` 누적은 읽기 중 active4.814MiB/20.58%, history4.088MiB/20.88%이며 대부분 위 Int.MarshalJSON을 포함한다. history의 작은 Clone 표본도 남아 있으므로 모든 복사/Marshal 할당이 사라졌다는 뜻은 아니다. 정수 Marshal은 이미 21byte 용량에 직접 숫자를 쓰며 공개 메서드가 반환하는 `[]byte`가 필요하다. 단순 문자열 중간값 제거를 다시 제안할 근거는 없다.

현재 `Int.UnmarshalJSON`은 정수마다 지역 string을 JSON으로 decode한 뒤 ParseInt한다. source line profile은 `internal/domain/integer.go:57`의 임시 string에 active16.19MiB·history12.25MiB(각5회 합)를 귀속한다. JSON의 공개 계약에는 escape된 숫자 문자열/leading zero 정규화 등이 있으므로 새 fast path가 이를 거절해서는 안 된다. 이것이 다음 후보이며 아직 제거 가능한 총량이나 시간 효과는 확정하지 않았다.

`parseValues`의 item 콜백 지역 DTO에 약13~14%가 귀속하고, 배열 자체도 약39%가 남았다. 배열은 이미 성공한 항목 수에 따른 bounded doubling이다. source line/할당 프로파일만으로 backing storage 전체가 불필요하다고 결론 내리지 않는다. 임시 DTO escape와 필수 최종 배열 저장을 분리해 검토할 수 있다.

CPU에서는 읽기 focus의 rawsyscalln flat이 active94.0ms/69.63%, history83.5ms/71.98%다. 보존한 caller 자료의 active SectionReader 표본1.88초 중1.87초는 TeeReader,0.01초는 CopyBuffer 경로다. CPU 표본 귀속이지 실제 pread 횟수나 디스크 대기 시간은 아니다. 기존 [읽기 버퍼 대조](decoder-read-buffer.md)에서 큰 버퍼의 일관된 개선을 확인하지 못했으므로 이 프로파일만으로 같은 큰 버퍼를 다시 채택하지 않는다. 두 checksum과 canonical 검증을 유지한다.

CPU worker에서 관찰한 stage wall 중앙값은 다음과 같다. 프로파일 CPU ms와 다른 측정량이고 stage 중앙값을 더하지 않는다.

| 상태 | checksum ms | decode ms | 주문 ms | book ms | 체결 ms | 최초 결과 ms | restore ms | replay/prefix ms | confirm ms |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| active | 3.970 | 161.964 | 48.902 | 7.554 | 0.016 | 105.044 | 10.958 | 35.015 | 41.243 |
| history | 3.584 | 136.448 | 25.683 | 0.003 | 10.380 | 100.491 | 10.781 | 30.365 | 36.109 |

두 상태의 최초 결과는 모두1만 개이며 여전히 큰 decode 구간이다. tail=0이어도 WAL prefix의 frame/CRC/정규형/중복·순번 검증을 수행하므로 WAL 할당이 남는다. WAL focus는 Open 전체 추정 할당의 active41.16%·history43.38%다. snapshot만 더 최적화해도 제거되지 않는 비용이며 prefix 검증 생략을 제안하지 않는다.

## 다음 후보와 채택 조건

| 순서 | 후보 | 근거와 범위 | 다음 검증 |
| --- | --- | --- | --- |
| 1 | Int.UnmarshalJSON의 정상 quoted ASCII digits 경로에서 임시 string decode 줄이기 | 읽기 자체2.450~3.238MiB/호출. 정수 한 경계부터 독립적으로 실험 | 외부 JSON의 whitespace/escape/leading zero·오버플로/음수/null/실패 시 수신자 보존·오류 분류를 기존 구현과 차등 검증. 정상 fast path 외에는 기존 parser에 위임하는 등 계약 유지. WAL/snapshot canonical 거절도 그대로 검사 |
| 2 | parseValues의 콜백 지역 DTO 재사용/소유권 개선 | 임시 객체 자체2.540~3.279MiB/호출. 최종 배열과 분리 | pointer/string 필드가 이전 항목과 별칭을 공유하거나 누락 필드를 이어받지 않도록 초기화/소유권 확인. 성공 항목 수 기반 용량 상한·오류 조기 종료·16KiB leaf/깊이 한도 유지 |
| 3 | WAL scan의 frame/record·검증 map 할당 수명 분석 | WAL 경로37.275~39.518MiB/호출. scan/Clone/validator가 여러 부분을 차지 | 먼저 line/객체 수별 소유권 분석. frame/CRC/부분 tail/중복/순번·최초 응답·writer 재개 계약을 줄이지 않음. 이번에는 구현하지 않음 |

생산 변경 후보는 하나씩 동결한다. 같은 active/history 각1만 명령·tail0/1천/5천 6조건에서 baseline/candidate 비계측 각5회, 총 할당·복구 시간·GC 뒤 heap·현재/누적 peak RSS와 전체 상태·후속 체결/취소·최초 응답 재시도를 비교해야 한다. 이번 profile의 비중은 우선순위 근거이지 개선 폭이나 채택 판정이 아니다.

## 실행·보존과 완료 상태

- Python 조정기에 입력 종류별 검증만 추가했다. 기존 tail 입력과 canonical/RSS candidate 입력을 지원하고 unknown 종류·smoke/full 혼합·생산 source 차이는 거절한다. 기존 collect/summarize와 Go/생산 코드는 그대로다. Python100개·입력 분기 회귀 및 작은4 worker 검증을 통과했다.
- 첫 smoke는 `cmd/.DS_Store`·`internal/.DS_Store`로 생산 파일 집합 검사가 거절했다. 원본은 `var/post-optimization-preflight-retained-20261113`로 옮겨 보존하고 첫 실패 `var/post-optimization-profile-smoke-20261113`와 로그를 남겼다. 새 `var/post-optimization-profile-smoke-retry-20261113`는 통과했다.
- 원본 `var/post-optimization-profile-full-20261113`의50 worker·642개 봉인 파일(약556.77MiB)과270개 source archive member를 검사했다. source/binary SHA·현재 생산 source·원본 RSS128파일이 일치했다. JSON/Markdown은 새 `var/post-optimization-profile-reanalysis-20261113`에서 bytes까지 같았다. 추가 caller/source line 자료는 `var/post-optimization-profile-details-20261113`, 도출/검증은 `var/post-optimization-profile-final-proof-20261113.json`에 보존했다.
- 전체 로컬 CI는 portable240개/23packages·native race279개/23packages(기존 child-helper skip1개), 각 Python100개와 진단/HTTP/저장/복구 smoke를 통과했다. 근거는 `var/ci/portable.trboN3`·`var/ci/native.zirjWm` 및 `var/post-optimization-ci-portable-20261113.log`·`var/post-optimization-ci-native-20261113.log`다. RSS 변경은 `dc7f10f`로 로컬 커밋했으며 구현·로컬 검증 완료 시점에는 후속 profile 변경이 기능 브랜치의 작업 트리였다. 이후 커밋/푸시·원격 CI는 별도 발행 단계에서 확인한다.

새10분 HTTP 부하·cold-cache·전원 장애 검증은 실행하지 않았다. p99·지속 처리량·누수 부재를 주장하지 않는다. 실제 전원 장애는 **미완료·보류**, v1 전체 계획은 **Blocked**를 유지한다.

후속 [정수 JSON 디코딩 비교](integer-json-decoding.md)에서 해당 첫 후보를 구현·검증했다. 이 보고서의 source/할당 비중은 그 변경 전 관측 시점의 기록이며 후속 코드를 다시 계측한 값이 아니다.

# 정수 JSON 디코딩 임시 할당 최적화

2026-11-13, 일반 숫자 문자열의 JSON decode 임시 string을 없애고 기존 공개 입력·오류·복구 계약을 유지했다. 같은 active/history 각1만 명령·이후0/1천/5천 명령의6조건을 각 버전5회 비교한 **60 worker 모두 전체 복구 bytes·최초480응답·중복 재시도120건·후속 거래/잔고 검증을 통과**했다. 정확한 Open TotalAlloc 중앙값은 **2.90~4.22% 감소**, Open wall 중앙값은 **7.37~9.01% 감소**했다. 할당 감소와 이번 고정 상태의 복구 시간 감소를 근거로 채택한다.

[실행·차등/퍼즈·재집계](../integer-json-decoding.md), [JSON](20261113-integer-json.json), [고정 빌드·검증·도출 수치](20261113-integer-json-artifacts.json)를 함께 읽는다. 프로파일/마이크로벤치·전체 API 비용은 서로 다른 측정이다. HTTP p99·지속 처리량 개선으로 확대하지 않는다. 실제 전원 장애는 **미완료·보류**다.

## 변경과 계약

[직전 프로파일](post-optimization-profiling.md)의 읽기 focus에서 Int.UnmarshalJSON 자체가 active3.238MiB/13.84%, history2.450MiB/12.51%를 차지했다. 이 비중은 후보 선택 근거이고 이번 개선율의 분모가 아니다.

생산 변경은 `internal/domain/integer.go` 하나다. 1~19자리 quoted ASCII digits를 전부 확인한 뒤 기존 ParseInt에 임시 string 변환을 넘긴다. ParseInt가 이를 보관하지 않아 프로젝트 Go1.27.3에서 임시 변환 할당이 없다. 모든 byte를 먼저 검사해 잘못된 JSON과 overflow가 섞인 경우의 오류 우선순위를 유지한다. 성공 때만 수신자에 대입하며 입력을 수정하거나 보관하지 않는다.

공백·escape·다른 길이/문법은 이전 JSON parser에 위임한다. `"0001"` 정규화·`"\u0031"` 허용·null/number/음수/초과 거절·ErrInteger/ErrOverflow 분류/문구·실패 시 값 보존을 유지한다. 공개 HTTP JSON의 허용 범위와 WAL/snapshot의 canonical 파일 거절 범위가 다르다는 계약도 유지한다. checksum/두 번째 해시/정규형 재비교·WAL prefix·strict sync·상태 소유권/용량 한도는 변경하지 않았다. 배열·읽기 버퍼·probe는 그대로다.

독립 legacy oracle로 직접 메서드와 json.Unmarshal 경유를 모두 대조했다. 경계/공백/escape/overflow 혼합/invalid UTF-8/초기값 보존 corpus와 고정seed20261113의2,000회 숫자·leading zero·escape·임의 bytes 조합을 통과했다. 30초·4 fuzz worker가 **3,196,674회**를 실행했고 차이가 없었다. 기존 domain/snapshot/WAL/strict JSON/API 테스트도 통과했다.

## 마이크로벤치

별도로 fuzz가 끝난 뒤 각5회·benchtime200ms 측정했다. ns/op는5회 중앙값이다. 함수 간접 호출·같은 전역 수신자·같은 입력으로 비교한다. allocation은 반복마다 아래 값이 같았다.

| 입력 | 기존 ns/op | 후보 ns/op | 기존 B/op·할당 횟수 | 후보 B/op·할당 횟수 |
| --- | ---: | ---: | --- | --- |
| 작은 일반 숫자 `"1234"` | 76.08 | 10.91 | 16·1 | 0·0 |
| 최대값 `"9223372036854775807"` | 102.20 | 22.72 | 16·1 | 0·0 |
| escape fallback `"\u0031"` | 119.20 | 118.60 | 24·2 | 24·2 |

첫 microbench는 fuzz 프로세스와 겹쳤다. `var/integer-json-microbench-20261113.log`를 보존하고 위 수치에는 쓰지 않았다. 전체 반복을 새 `var/integer-json-microbench-isolated-20261113.log`에서 다시 측정했다. 이는 다른 부하를 통제한 전용 장비의 결과는 아니며 정상 입력의0할당과 fallback 경로 보존을 확인하는 근거다.

## 고정 상태와 환경

- 기준 revision `dc7f10fe242f268f16233ee67eb4f4de698b4f06`·기능 브랜치 `codex/integer-json-decoding`의 dirty 작업 트리다. 이전 미커밋 profile 도구/문서를 보존했다. baseline 생산 코드는 master24bc995와 같고 candidate는 integer.go만 다르다. 두 빌드의273개 source archive member를 검증했고 전체 source 차이도 integer.go 하나뿐이다. 테스트/도구/Go probe는 양쪽이 같다. 정확한 source/archive/binary SHA·Go 환경은 artifact JSON에 있다.
- 입력 `var/canonical-buffer-full-20261113`은 기존60 worker와 전체 봉인435개 파일을 다시 검증한 원본이다. candidate 생산 source와 이번 baseline을 대조했다. 두 상태의 final/prefix snapshot·전체 WAL·Genesis·lock가 같고 반복마다 동일 runId/초기 자금이다. active는 주문9,995/활성9,992/체결1건, history는 주문4,999/활성0/체결2,499건이며 최초 결과는 각각1만 개다.
- Apple M3 Pro·11CPU·18GiB, macOS15.8 arm64/APFS·Go1.27.3/cgo1, GOMAXPROCS11/GOGC100/GOMEMLIMIT2GiB다. 상태/조건·버전 순서를 교대하고 새 프로세스를 사용했다. CPU/할당 profiler나 RSS 유휴/강제 OS 반환 진단은 끈 기존 tail-measure --resident probe다. 기존 GC 뒤 메모리 관측은 Open 밖이다.
- warm-cache·CPU 유휴 gate 없는 비격리 호스트다. 전체 비교 중 다른 테스트/계측을 실행하지 않았고 느린 표본을 제외하지 않았다. full을 마친 뒤 전체 CI를 시작했다. 2GiB 메모리 관측 budget·4GiB 묶음 상한·별도8GiB 여유·128MiB 준비 gate를 유지했다.

## 전체 복구 비용

각 버전 5회, 60 worker. 중앙값 [최소~최대].

| 상태 | 이후 명령 | 이전 Open ms | 이후 Open ms | 변화 % | 이전 TotalAlloc MiB | 이후 TotalAlloc MiB | 변화 % |
| --- | ---: | --- | --- | ---: | --- | --- | ---: |
| active | 0 | 287.307 [281.398~331.305] | 266.124 [258.145~284.354] | -7.37 | 95.894 [95.877~95.922] | 91.852 [91.824~91.866] | -4.22 |
| active | 1000 | 264.459 [260.046~268.088] | 240.641 [240.593~252.074] | -9.01 | 92.417 [92.410~92.449] | 88.677 [88.655~88.683] | -4.05 |
| active | 5000 | 178.213 [176.783~186.248] | 164.577 [163.656~167.064] | -7.65 | 74.493 [74.468~74.511] | 71.985 [71.969~71.997] | -3.37 |
| history | 0 | 248.447 [242.509~251.005] | 227.315 [227.115~229.708] | -8.51 | 83.613 [83.583~83.629] | 80.409 [80.397~80.439] | -3.83 |
| history | 1000 | 228.621 [223.324~230.559] | 209.057 [206.525~215.334] | -8.56 | 81.204 [81.198~81.245] | 78.250 [78.227~78.275] | -3.64 |
| history | 5000 | 155.232 [152.303~157.622] | 143.461 [142.564~147.697] | -7.58 | 66.191 [66.162~66.213] | 64.269 [64.239~64.277] | -2.90 |

| 상태 | 이후 명령 | 이전 GC 뒤 heap MiB | 이후 GC 뒤 heap MiB | 이전 GC 뒤 RSS MiB | 이후 GC 뒤 RSS MiB | 이전 peak RSS MiB | 이후 peak RSS MiB |
| --- | ---: | --- | --- | --- | --- | --- | --- |
| active | 0 | 10.912 [10.911~10.913] | 10.871 [10.871~10.871] | 45.906 [44.984~46.344] | 44.203 [43.469~44.469] | 45.906 [44.891~46.250] | 44.203 [43.375~44.453] |
| active | 1000 | 10.860 [10.859~10.876] | 10.820 [10.819~10.822] | 43.312 [42.000~44.156] | 42.547 [41.047~45.047] | 43.266 [42.000~43.938] | 42.547 [41.031~45.031] |
| active | 5000 | 10.658 [10.657~10.658] | 10.638 [10.637~10.650] | 35.234 [33.359~37.047] | 34.359 [34.031~35.922] | 35.234 [33.328~37.047] | 34.328 [33.969~35.719] |
| history | 0 | 9.623 [9.621~9.627] | 9.631 [9.628~9.632] | 42.375 [40.984~45.609] | 41.406 [40.578~41.906] | 42.344 [40.953~45.609] | 41.406 [40.547~41.844] |
| history | 1000 | 9.646 [9.644~9.672] | 9.656 [9.655~9.662] | 40.188 [38.844~40.562] | 39.453 [39.094~39.750] | 40.031 [38.828~40.547] | 39.375 [39.078~39.578] |
| history | 5000 | 9.502 [9.502~9.524] | 9.523 [9.522~9.538] | 33.438 [32.797~33.734] | 33.188 [32.484~34.672] | 33.391 [32.797~33.703] | 33.141 [32.484~34.656] |

준비/해시/강제 GC/후속 검증은 Open wall 밖이다. 자연 GC·내부 I/O 대기는 포함한다.
heap은 엔진을 살려 둔 강제 GC 뒤 HeapAlloc, GC 뒤 RSS는 Mach resident_size, peak RSS는 worker 시작부터 Open 직후까지 high-water다. 2GiB budget 유지.
동일 입력·32KiB decoder·canonical/이중 checksum·WAL prefix/최초 응답/전체 상태·strict sync 유지. 느린 표본도 포함.
warm-cache·비격리 호스트이며 HTTP p99/최대 처리량 개선을 증명하지 않는다. 실제 OS·전원 장애 검증은 미완료·보류다.

## 해석과 한계

6조건 모두5쌍에서 할당과 Open wall이 감소했다(각30/30쌍). 이후 명령이 늘면 snapshot에 담긴 정수 수가 줄어 절대 할당 감소가 작아지지만 WAL도 같은 Int를 사용한다. 원인의 세부 비중을 이번 비계측 측정만으로 분리하지 않는다. 손상/거절·최초 결과·후속 체결/취소/예약 반환과 전체 bytes를 유지해 이번 후보를 채택한다.

GC 뒤 live heap 중앙값 차이는 active −41.70/−40.02/−20.26KiB, history +7.83/+10.18/+21.67KiB다(tail0/1천/5천 순). 즉 history 유지 heap은 소폭 증가했다. current RSS 중앙값은6조건 모두0.250~1.703MiB 낮았지만 **개별21쌍 감소·9쌍 증가**했고 최대 증가는 active tail5천 반복2의+2.5625MiB였다. 최저 변화는 history tail0 반복3의−4MiB다. 누적 peak RSS 중앙값도6조건 낮았으나 current RSS와 측정 시점이 다르다. 모든 표본을 보존하며 메모리 일괄 개선·누수 부재를 주장하지 않는다. [RSS 진단](recovery-rss.md)의 유휴/GC/footprint/압축 관측과 혼합하지 않는다.

이번 시간은 공개 Open의 wall이다. snapshot 읽기·WAL prefix/재생·confirm를 포함하며 프로파일 CPU ms나 HTTP 주문 응답 시간과 다르다. 장기간 누적 실행·10분 HTTP 부하·다른 상태 크기·cold-cache·실제 전원 장애는 새로 검증하지 않았다. 전원 장애는 미완료·보류, v1 전체는 Blocked다.

## 검증·보존과 완료 상태

- 비교 runner는 기존 배열 비교의 검증/worker/receipt를 재사용한다. 입력 producer를 canonical candidate로, 허용 생산 차이를 integer.go로 지정할 수 있도록 확장했고 기존 기본값/과거 report 형식은 유지했다. 새 wrapper는 다른 입력 종류를 거절한다. source/probe/toolchain 차이·누락/추가/변조 worker·잘못된 경로/메모리·계약 위반은 기존 검사에서 거절한다. 새 입력 분기·변경 범위/producer 선택 회귀를 추가했고 전체 Python103개를 통과했다. 기존 RSS 지원 배열/canonical full 보고서도 현재 공유 검사기의 재집계가 동일했다(`var/integer-json-historical-reanalysis-v2-20261113.log`). 초기 검사기의 RSS 필드 도입 전 자료 선택 실패 로그도 보존했으며 그 과거 schema는 이번 호환 검증 범위에서 제외했다.
- 첫 독립 oracle 테스트는 JSON 경유 수신자를 비포인터로 넘겨 실패했다. 수신자를 고치고 JSON 외부 문법 오류의 정확한 타입/문구와 두 domain sentinel을 비교하도록 수정했다. 실패 로그 `var/integer-json-domain-baseline-20261113.log`는 보존했다. 수정 뒤 baseline과 candidate 모두 차등 검증을 통과했다. 생산 구현의 회귀 실패는 없었다.
- 작은 `var/integer-json-smoke-20261113`의12 worker와 full `var/integer-json-full-20261113`의60 worker를 검증했다. full은327개 봉인 파일·약57.93MiB이고 성공 scratch만 receipt를 남기고 제거했다. 모든 입력/두 고정 빌드/실패는 보존했다. archive 각273개 member·현재 candidate source·전체 source 차이 한 파일·원본 봉인을 확인했다.
- 새 `var/integer-json-reanalysis-20261113`에서 JSON/Markdown을 재생성해 원본과 bytes까지 일치했다. 별도 실행/원본 입력 접근 없이 두 빌드/receipt/관측만 검증한다. 상세 증거는 `var/integer-json-final-proof-20261113.json`과 동명의 문서 artifact JSON이다.
- 전체 로컬 CI는 portable241개/23packages·native race280개/23packages(기존 child-helper skip1개), 각 Python103개·진단/HTTP/저장/복구 smoke를 통과했다. 근거는 `var/ci/portable.ULWU7D`·`var/ci/native.y5R7pO`, `var/integer-json-ci-portable-20261113.log`·`var/integer-json-ci-native-20261113.log`다. 위 완료 기록은 구현·로컬 검증 시점이다. 이후 커밋/푸시·원격 CI는 별도 발행 단계에서 확인한다.

# 스냅샷 생성·복구 CPU·메모리 할당 분석

2026-11-13, `codex/snapshot-profiling`에서 기존 [상태 비용 측정](state-size-costs.md)의 **동일한 active/history 각 10,000명령 fixture**를 사용했다. 새 worker 190개 모두 복구 경로·완전한 canonical snapshot/WAL/Genesis/lock bytes 검증을 통과했다. production 코드는 바꾸지 않았으며 **다음 최적화 후보는 WAL 레코드의 반복 JSON 검증 경로**다. 아래 비중은 관찰 근거이며 개선 폭의 예측이 아니다.

[명령·프로파일 해석](../snapshot-profiling.md), [기계 판독 요약/해시](20261113-snapshot-profiling.json)를 함께 읽는다. 개별 profile·합친 protobuf·전체 함수표는 로컬 봉인 자료에 보존했다.

## 조건과 재현

- 기준 revision `b57a6d045380de228fa93bc00720a09a1ae37339`, 새 probe/조정기는 미커밋 작업 트리에서 빌드했다. 소스 233개를 동결했고 production 영역/Go 설정 155개의 SHA-256이 이전 비용 측정과 같다. 새 바이너리는 계측 코드가 추가되므로 이전 측정 바이너리와 같은 해시라는 뜻은 아니다.
- Apple M3 Pro·11 CPU·18GiB, macOS 15.8 arm64/APFS, Go 1.27.3/cgo=1. GOMAXPROCS=11/GOGC=100/GOMEMLIMIT=2GiB, cache flush 없는 warm-cache·비격리 개발 호스트다. 입력 준비·복사/해시·후속 검증은 profile 밖이다. 계측 중 호스트 CPU 유휴 gate를 적용한 처리량 실험은 아니다.
- 기존 active는 주문 10,000개/활성 10,000개/체결 0개, history는 주문 5,000개/활성 0개/체결 2,500개다. 1,000/5,000 크기는 이번 profile에 포함하지 않았다. 같은 N에서 payload 구성도 다르므로 활성 상태 하나의 인과 효과로 해석하지 않는다.
- WAL Open/snapshot Open은 CPU 각 20회, 새 snapshot 생성은 각 40회, allocation은 각 경로 5회다. 프로파일러별 별도 새 프로세스와 사본으로 총 190 worker다. snapshot Open tail=0이며 전체 prefix 검사·재확인/sync를 유지했다. create의 사전 WAL Open은 계측 밖이다.
- CPU는 10ms sample period. allocation은 64KiB 샘플링의 호출 전후 profile 차이이며 두 GC로 accounting을 반영했다. API 전후 TotalAlloc counter도 따로 보존한다. allocation 전역 차이에는 profile 직렬화·런타임의 부수 비용이 남을 수 있어 internal 호출 경로 focus와 전체 표를 구분한다.
- CPU 전체 표에는 자연 GC·scavenger·scheduler와 직전 GC에서 이어진 비동기 작업, 호출 전후 counter 읽기·profile 관리 비용도 포함될 수 있다. internal focus는 해당 stack을 거친 표본이며 전체 CPU의 귀속 비용이 아니다. 아래 시간은 **CPU profile 표본 합계/호출 수**로, wall-time 중앙값이 아니다. 기존 비계측 결과와 전후 개선 비교에 쓰지 않는다.

```sh
python3 tools/diagnostics/snapshot_profile.py run --bundle var/state-scale-full-20261113 --output var/snapshot-profile-full-new
python3 tools/diagnostics/snapshot_profile.py analyze --bundle var/snapshot-profile-full-new --output var/snapshot-profile-reanalysis-new
```

## 경로별 표본과 할당

CPU의 두 값은 전체/internal focus 표본 합계를 각각 반복 수로 나눈 ms다. sample 수는 전체 합계다. 할당은 5회 차이 profile을 합친 internal 경로 추정량을 5로 나눈 MiB다.

| 유형 | 경로 | CPU 반복 | 전체 sample 수 | 전체 CPU ms/호출 | internal CPU ms/호출 | internal 추정 할당 MiB/호출 |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| active | wal-open | 20 | 456 | 228.000 | 123.500 | 123.844 |
| active | snapshot-open | 20 | 942 | 471.000 | 311.000 | 195.131 |
| active | create | 40 | 241 | 60.250 | 25.000 | 29.299 |
| history | wal-open | 20 | 395 | 197.500 | 94.500 | 111.811 |
| history | snapshot-open | 20 | 809 | 404.500 | 269.500 | 171.531 |
| history | create | 40 | 205 | 51.250 | 22.750 | 24.213 |

create의 internal CPU sample은 active 100개/history 91개로, 전역 241/205개보다 적다. 작은 함수 사이의 미세한 순위 차이나 단일 표본의 10ms를 일반적인 지연으로 해석하지 않는다. 반복 40회는 CPU 표본 확보 목적이며 비계측 5회와 표본 집단이 다르다.

## 함수별 병목 근거

아래 비중은 각 경로의 **internal focus 할당/CPU 합계**를 분모로 하는 cumulative 값이다. 부모/자식은 중첩되므로 더하지 않는다. 생성의 bytes.Clone과 Int.MarshalJSON은 json.Marshal 경로의 일부다.

| 관찰 경로 | 함수/호출 경로 | active 할당 비중 | history 할당 비중 | active CPU 비중 | history CPU 비중 |
| --- | --- | ---: | ---: | ---: | ---: |
| wal-open | WAL 레코드 디코딩 | 75.15% | 75.21% | 47.37% | 52.38% |
| wal-open | WAL의 strictjson.Decode | 60.01% | 60.67% | 40.08% | 45.50% |
| snapshot-open | 복구의 WAL 레코드 디코딩 | 47.96% | 49.02% | 21.86% | 22.08% |
| snapshot-open | 복구의 strictjson.Decode | 38.67% | 40.09% | 18.01% | 18.55% |
| create | 생성의 json.Marshal | 58.65% | 60.72% | 10.00% | 10.99% |
| create | 생성의 정수 JSON | 15.40% | 14.61% | 2.00% | 2.20% |

**복구:** `wal.decodeRecord`는 외부 commandWire와 내부 Place/Cancel payload에 `strictjson.Decode`를 각각 호출한다. strictjson은 토큰/중복 키 순회 → reflect 기반 shape/map 검증 → typed Unmarshal을 거치고, WAL은 다시 canonical encode와 원본 bytes 비교를 한다. prefix도 검사하는 현 계약 때문에 snapshot이 있어도 이 비용이 남는다. 복구 단계에서 WAL scan 경로의 할당 비중은 active 57.10%/history 58.90%이고, 위의 decodeRecord/strictjson은 그 내부 부분이다. snapshot의 leaf decode·canonical 재인코딩·Restore와 confirm 비용도 존재하므로 WAL만 고치면 전체 복구가 해결된다고 주장하지 않는다.

**생성:** `json.Marshal` 경로의 추정 할당은 active 58.65%/history 60.72%다. `bytes.Clone` 자체가 32.44%/34.17%, Int.MarshalJSON 자체가 15.40%/14.61%를 차지했다. 현재 encoder는 각 leaf DTO마다 json.Marshal의 임시 bytes를 만든 뒤 writer로 내보낸다. 이미 제거한 크기 사전 계산의 중복 직렬화와는 다른 남은 할당이다. owner CaptureWithTimings 경로는 19.44%/19.11%이며 분리된 사본의 소유권 계약 때문에 단순 제거할 수 없다.

**CPU와 I/O 경계:** internal focus의 syscall.rawsyscalln 자체 비중은 WAL Open 34.01%/25.93%, snapshot Open 54.18%/50.83%, create 72.00%/68.13%였다. 추가 pprof traces에서 active create는 write/buffer flush 및 Statfs, snapshot 복구는 read/pread·decoder buffer refill·기존 파일 비교 스택이 관찰됐다. 이는 커널/장치 지연의 원인 확정이나 F_FULLFSYNC 대기 ms 측정이 아니다. 전역 CPU의 runtime.madvise·GC/scanner/대기 표본도 보존했으며 이를 decoder 함수의 자체 CPU로 몰아 합산하지 않는다.

## 다음 최적화 후보와 채택 조건

| 우선순위 | 후보 | 근거와 제한 | 후속 검증 |
| --- | --- | --- | --- |
| 1 | WAL 레코드 전용 canonical decoder로 반복 검증/중간 객체 줄이기 | 양쪽 복구에 공통이며 snapshot Open 할당 약 48~49%가 decodeRecord 경로다. HTTP용 범용 strictjson은 유지하고 폐쇄된 WAL DTO/이미 요구되는 canonical bytes 대조를 활용하는 별도 후보부터 검토한다. typed decode·의미 검증·canonical 대조는 남아야 함 | 원본·후보의 정상/손상 corpus 판정 일치, golden/fuzz/native 복구 및 아래 계약 검증 후 비계측 전후 각 5회 |
| 2 | snapshot leaf 직렬화를 재사용 가능한 버퍼/타입별 writer로 전환 | 생성 json.Marshal 할당 약 59~61%; bytes 복사와 정수 임시 bytes가 크다. CPU 대부분을 직접 차지한다고 확정하지 않으며 arbitrary string의 escaping·필드 순서·크기 계산과 실제 encode 일치를 유지해야 함 | 전체 파일 bytes 동일, 거절/크기 상한/부분 쓰기/게시 실패 검증, 전후 5회 wall·할당 비교 |
| 3 | snapshot read/write/compare 버퍼와 syscall 호출 수 대조 | read/pread/write 스택 표본이 크다. 32KiB buffer 크기·비교 읽기의 호출 수와 비용을 한 변수씩 대조할 가치가 있음. 이번 자료만으로 디스크 병목·최적 버퍼 크기는 미확정 | bounded buffer 메모리·value 한도 유지, 실제 syscall 수/크기 별도 계측, 같은 bytes·전후 5회 검증 |

첫 후보는 **WAL 전용 디코더**로 추천한다. duplicate/unknown/missing/case-folded/reordered 필드, 잘못된 null/정수/UTF-8/escape, canonical 여부, framing/순번/run identity/최초 명령 대조, 완전 그룹 손상과 불완전 EOF tail 판정이 기존과 같아야 한다. 범용 HTTP 입력 허용 범위를 이 변경으로 좁히거나 넓히지 않는다. 전체 WAL prefix 검사·snapshot canonical confirm·파일/디렉터리 엄격 sync를 생략하는 방안은 이 후보에 포함하지 않는다.

후속 성능 판정은 같은 fixture 사본·같은 환경·비계측 원본/후보 각 5회에서 공개 Open/Snapshot 전체 시간과 단계/TotalAlloc·post-GC 상태를 비교한다. 느린 표본도 보존하고 생성/복구 전체 bytes와 최초 결과가 같아야 한다. 필요하면 별도 profile로 할당 이동을 확인하며 HTTP p99/최대 처리량 개선은 별도 실험 없이는 주장하지 않는다. 이번 작업에서 production 최적화는 구현하지 않았다.

## 검증과 보존 자료

- `var/snapshot-profile-full-20261113`: 190개 worker, 2,126개 파일 봉인 검증. source 233개 archive와 수집 종료 시점 source SHA 일치, production 155개 bytes 이전과 일치. 바이너리 SHA-256 `b908d7211bbefe2eff4150b2e3814f7036616a30b4acac07d9044ebb96d6d35d`.
- 입력 `var/state-scale-full-20261113`의 588개 파일 봉인을 전후 다시 검증했다. fixture 6개 중 1만 명령 두 개만 복사하며 이전 실행을 덮어쓰거나 삭제하지 않았다. 본 profile 묶음은 약 2GiB, 종료 여유 약 12GiB로 4GiB/8GiB 예산 안이다.
- `var/snapshot-profile-reanalysis-20261113`: 원본 profile만 읽어 새 JSON/Markdown·pprof 그룹 생성, 182개 파일 검증. report.json/report.md는 본 실행과 bytes가 같다.
- `var/snapshot-profile-callers-20261113`: 추가 syscall pprof peek/traces와 봉인. `var/snapshot-profile-final-proof-20261113.json`에 원본/새 자료·archive·재집계 일치의 마지막 검증 기록.
- 회귀: Go native profile 파일/소유권/출력 재사용·세 경로 상태 검사, Python 61개(일정/반복·단위/zero-flat cumulative·잘못된 일정 사전 거절 포함), 12 worker 기능 smoke 통과. 작은 CPU smoke의 표본 0은 기능 확인이며 큰 실행의 병목 판단에 섞지 않았다.
- 로컬 전체 CI: portable 224개/23 packages·native race 262개/23 packages(기존 명시된 child-helper skip 1개), 각 Python 61개와 진단/저장/복구 smoke 통과. `var/ci/portable.x4dMuC`, `var/ci/native.5zVizC` 및 `var/snapshot-profile-ci-portable-nativeperm-20261113.log`, `var/snapshot-profile-ci-native-20261113.log`에 보존했다.
- 초기 `var/snapshot-profile-smoke-final-20261113`은 sandbox의 sysctl 제한, 최초 portable CI `var/ci/portable.zyGEcT`는 loopback bind 제한으로 실패했다. 코드 오류로 숨기거나 삭제하지 않고 권한 있는 새 경로에서 같은 검증을 완료했다. 최종 smoke는 `var/snapshot-profile-smoke-native-20261113`이다.

실제 OS·전원 장애 검증은 **미완료·보류**이고 v1 전체 계획은 계속 Blocked다. 이 작업의 완료는 프로파일링·병목 분석·후속 후보 정리이며 운영 내구성/성능 보장의 완료가 아니다.

마스터 반영 전 검토에서 조정기의 준비 공간 검사를 출력/빌드 이전으로 앞당기고 fixture 복사 직전의 실제 크기 예산 검사와 Python 회귀를 추가했다. 보관 source archive는 수집 시점 자료로 유지하며 변경된 두 Python 파일과 구분한다. 동결 Go probe·production 코드는 그대로이고 기존 190개 측정 수치는 수정하지 않았다.


검토 수정 후 검증은 별도 로그/새 출력에 남겼다. `var/ci/portable.C71fFA`·`var/ci/native.GfKd1n`의 Go portable 224개/native race 262개, 각 Python 62개·진단 smoke와 `var/snapshot-profile-review-smoke-20261113`의 12 worker가 통과했다. `var/snapshot-profile-review-reanalysis-20261113`에서 기존 원본을 읽어 재생성한 JSON/Markdown bytes도 같았다.

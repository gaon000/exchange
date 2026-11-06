# 낮은 요청률 기준과 정수 JSON 비교

2026-11-06, 정상 10,000 requests/s는 기존 P6 기준을 **3/3 통과**해 후속 비교 작업점으로 채택했다. snapshot 1,000 requests/s는 **2/3 통과**여서 미채택이다. 정상 조건에서 정수 JSON의 중첩 직렬화를 제거한 후보도 3/3 통과했다. 계약과 단위 할당 감소를 확인해 변경을 유지하며, 최대 지속 처리량 향상이나 지연의 인과 효과를 주장하지 않는다.

[기계 판독 결과](20261106-lower-rate-integer-comparison.json)는 9개 실행의 판정·구간·해시, 비교 지표·조건, 유휴 관측과 검증을 보존한다. [비교 명세](../../config/comparisons/lower-rate-integer-v1.json)는 자동 JSON·Markdown 보고서의 입력이다. 원본 표본·WAL·생산 빌드는 Git 제외 `var/`에 보관한다.

## 조건과 원본 보존

기준 생산 revision은 `086c61a`, 후보는 `65ca1ba`다. Go 1.27.3·macOS arm64/APFS·AC 전원, GOMAXPROCS=11/GOGC=100/GOMEMLIMIT=off를 고정했다. 초기 설정·seed·요청 혼합·warmup 5초/본 측정 20초·500ms 서버 계측·30초 cooldown과 기존 criteria-v1을 유지했다. 요청 p99 및 각 5초 구간 20ms, 미전송·운영 오류 0, 최소 독립 3회 조건을 완화하지 않았다. snapshot 조건은 주기 5초다.

첫 collection의 정상1/snapshot1을 보존하고 `lower-rate-resumed-3`에서 snapshot2→정상2→정상3→snapshot3을 이어갔다. 원래 snapshot1 실패도 그대로 포함한다. 앞선 세 collection의 유휴 검사 실패 24개와 완료 prefix·참조 manifest hash를 유지했다. 사용자에게 부하를 줄여 달라고 요청한 뒤 재개했으며 후속 네 실행은 각각 첫 관측에서 유휴 기준을 통과했다. 전체 baseline의 유휴 관측은 30개, 통과 6개/거절 24개다.

후보는 정상1→정상2→정상3을 새 run으로 실행했다. 첫 실행의 첫 관측은 busy 중앙값 16%로 거절했고 다음 관측부터 실행했다. 후보 유휴 관측은 4개, 통과 3개/거절 1개다. 모든 실행 직전 최소 10개 1초 관측에서 중앙값 ≤15%·최대 ≤30%를 요구했다. 시스템 서비스·다른 앱을 중단하거나 설정을 바꾸지 않았다. 이 gate는 실행 중 스케줄링·I/O 지연이나 완전한 호스트 격리를 보장하지 않는다.

## 기준 6회 판정

모든 실행의 표본 재집계와 원본 검증을 통과했고 측정은 유효하다. 아래 p99는 본 측정 요청 cohort 값이다.

| 조건/실행 | 요청 p99, ms | 최악 5초 구간 p99, ms | 판정 |
| --- | ---: | ---: | --- |
| 정상 10,000/s, 1 | 7.187708 | 8.003875 | 통과 |
| 정상 10,000/s, 2 | 7.207208 | 8.981750 | 통과 |
| 정상 10,000/s, 3 | 7.032291 | 7.079917 | 통과 |
| snapshot 1,000/s, 1 | 10.799209 | 39.254708 | WINDOW_3_P99 실패 |
| snapshot 1,000/s, 2 | 10.517625 | 12.650625 | 통과 |
| snapshot 1,000/s, 3 | 10.362708 | 11.745708 | 통과 |

정상은 3/3 통과·qualified=true다. snapshot은 전체 p99가 20ms 아래여도 첫 실행의 마지막 구간이 실패해 qualified=false다. 낮은 snapshot 요청률도 지연 기준 통과를 보장하지 않으며 이 실패의 단일 원인은 확정하지 않았다. 두 조건은 요청률과 누적 상태 크기가 달라 같은 상태의 snapshot 오버헤드 비교가 아니다.

## 정수 JSON 변경과 검증

`Int.MarshalJSON`에서 `json.Marshal(n.String())` 대신 따옴표와 비음수 십진수 bytes를 직접 append한다. 기존 JSON 문자열 형식·정수 정밀도·음수 ErrInteger를 유지하며 decode·잔고·주문·WAL/snapshot 규칙은 변경하지 않았다. 새 의존성은 없다.

테스트는 최적화 이전 encoder를 독립 oracle로 보존해 0·최대 int64·음수/최소 int64와 고정 seed 1,000값의 bytes/오류를 비교한다. 기존 wire 경계·golden과 전체 native 저장/복구 검증도 통과했다.

19자리 양수 하나를 CPU 1개·500ms·각 5회 측정한 실행별 중앙값이다. 실제 값의 자릿수·상위 encoder와 동시 부하 비용을 대표하지 않는다.

| 구현 | ns/op 중앙값 | bytes/op | allocs/op |
| --- | ---: | ---: | ---: |
| 이전 encoder | 128.1 | 80 | 4 |
| 직접 append | 20.53 | 24 | 1 |

## 정상 조건 전체 프로그램 비교

후보의 요청 p99는 7.037084/7.181792/7.004375ms, 최악 구간 p99는 7.206292/7.305042/7.071166ms이며 모두 통과했다. 기준·후보의 독립 run ID는 겹치지 않고, 각 측은 하나의 P6 series로 3/3 통과했다. 자동 보고서는 기록된 조건 일치, 원본 재계산·무결성·빌드 일관성을 확인해 eligibleForReview=true, improvementClaimed=false로 판정했다.

아래는 실행별 값의 중앙값이며 여러 실행의 요청 표본을 합쳐 p99를 다시 계산하지 않았다. CPU·RSS는 준비/측정/drain을 포함한 서버 프로세스 생애, 지연·처리량은 본 측정 cohort다.

| 지표 | 기준 3회 중앙값 | 후보 3회 중앙값 | 관찰 변화 |
| --- | ---: | ---: | ---: |
| 요청 p99 | 7.187708ms | 7.037084ms | -2.10% |
| 예정 시각 기준 p99 | 7.248292ms | 7.082042ms | -2.29% |
| 5초 구간 요청 p99 최댓값 | 8.003875ms | 7.206292ms | -9.96% |
| 구간 내 완료 신규 승인 | 6,504.00/s | 6,503.45/s | -0.0085% |
| 서버 생애 CPU 증분 | 14.411655s | 13.423203s | -6.86% |
| 서버 생애 peak RSS | 426.8125MiB | 421.984375MiB | -1.13% |
| 생성기 미전송 | 0 | 0 | 동일 |

이전·후보 요청 p99의 관측 범위가 겹치며 각 3회인 순차 실행이다. 기준의 일부는 호스트 부하를 줄이기 전에 수집됐고, 캐시·OS background 작업을 완전히 격리하지 못했다. 또한 서버와 생성기에 같은 정수 코드 변경이 적용된 clean 빌드를 사용해 서버만의 기여를 분리하지 않았다. 고정 10,000/s 작업점의 관찰이고 포화점 탐색이나 최대 처리량 비교는 수행하지 않았다. 단위 할당 감소와 회귀 검증을 근거로 작은 변경을 유지하며 전체 지연 향상의 인과 효과·통계적 유의성은 주장하지 않는다.

## 재현과 검증

기존 baseline/재개 parent는 모두 보존한다. 다음 명령의 build/output은 매번 새 이름을 사용한다. 현재 source를 새로 빌드하면 당시 revision/바이너리 해시와는 달라지므로 실제 새 provenance를 기록한다.

```sh
python3 tools/diagnostics/experiment.py baseline-build --output var/lower-rate-int-build
python3 tools/diagnostics/experiment.py baseline --build-dir var/lower-rate-int-build --output var/lower-rate-integer-candidate --preset reference --normal-rate 10000 --snapshot-rate 1000 --condition normal --quiet-attempts 15
python3 tools/diagnostics/experiment.py verify --bundle var/lower-rate-integer-candidate --output var/lower-rate-integer-candidate-integrity.json
python3 tools/diagnostics/experiment.py compare --spec config/comparisons/lower-rate-integer-v1.json --output var/comparisons/lower-rate-integer-v1
python3 tools/diagnostics/experiment.py verify --bundle var/comparisons/lower-rate-integer-v1 --output var/comparisons/lower-rate-integer-v1-verified.json
scripts/go test ./internal/domain -run '^TestIntegerMarshalMatchesLegacyContract$' -bench '^BenchmarkIntegerMarshalJSON$' -benchmem -benchtime=500ms -count=5 -cpu=1
```

Python 일반/`-O` 각각 27개, portable Go 206개, native race 238개와 실제 저장·HTTP·복구/재시도·진단/비교/변조 smoke를 통과했다. baseline의 네 collection과 재개 참조, 후보·자동 보고서 inventory를 검증했다. raw benchmark와 local CI 로그는 `var/lower-rate-int-production-bench.txt`, `var/lower-rate-int-ci-{portable,native}.log`에 있다. 원격 CI 결과는 실제 push commit에 별도로 연결한다.

새 스냅샷 성능 비교·10분 장시간 검증은 이번 작업 범위에 추가하지 않았다. 실제 전원 장애 검증은 계속 보류다.

# 정상 세 요청률 재측정

2026-11-09 추가 측정 재개 요청에 따라 2,000/3,000/5,000 requests/s를 각 독립 3회 다시 실행했다. **신규 9회에서는 2,000/s와 3,000/s가 각각 3/3 통과, 5,000/s는 1/3 통과**다. 같은 생산 빌드의 이전 실패/중단을 포함한 **누적 24회에서는 2,000/s만 qualified=true**다. 과거 실패를 이번 통과로 대체하지 않았다.

[기계 판독 결과](20261109-normal-capacity-recheck.json)에 신규/누적 그룹, 24개 run ID·판정·무효 사유, 새 9회의 표본 재집계·창별 지표·호스트·원본/보고서 hash를 보존한다. [이전 15회](snapshot-window-capacity.md)는 당시의 관찰 경계 기록이다.

## 같은 조건과 보존

기존 clean 생산 빌드 7b9e720의 exchange/loadgen hash를 그대로 사용했다. 초기 잔고·seed·요청 혼합·warmup 5초/측정 20초·P6 criteria-v1·20ms/5초 창·미전송/운영 오류 0·GOMAXPROCS=11/GOGC=100/GOMEMLIMIT=off·500ms 서버 계측과 30초 cooldown을 유지했다. snapshot·trace·추가 생성기 진단은 끈다. source/production dependency는 변경하지 않았다.

새 실행 순서는 2,000→3,000→5,000/s의 각각 3회다. 매회 시작 전 최소 10개 interval에서 busy 중앙값 ≤15%·최대 ≤30%, AC 전원·저장 probe·RSS/디스크/새 묶음 예산을 확인했다. 유휴 대기 관측 최대는 15회이며 기준은 완화하지 않았다. 첫 2,000/s 실행은 10개 관측을 거절한 뒤 11번째에 시작했다. 전체 거절은 2,000/s 10개, 3,000/s 0개, 5,000/s 5개로 모두 보존했다.

시작 전 여유 공간은 약 15.73GiB, 실행 중 관찰 최저는 약 14.62GiB였다. 이전 실험·실패 자료는 삭제하거나 다시 봉인하지 않았다. 모든 새 collection inventory 검증과 24개 독립 run ID/같은 바이너리·기존 15개 판정 보존을 확인했다. 새 측정 8개는 measurementValid=true이고 1개는 생성기 누락으로 false다. 수집 완료와 측정/성능 통과를 구분한다.

## 신규 9회

요청 p99는 본 측정 cohort, 창 p99는 각 5초 cohort의 최댓값이다. 요청 p99만 낮아도 예정 시각 기준 p99 또는 미전송으로 실패할 수 있다.

| 요청률·실행 | 전체 요청 p99, ms | 최악 창 요청 p99, ms | 최악 창 예정 p99, ms | 결과 |
| --- | ---: | ---: | ---: | --- |
| 2,000/s, 1 | 9.132417 | 9.193417 | 9.313542 | 통과 |
| 2,000/s, 2 | 8.286708 | 9.529750 | 9.821083 | 통과 |
| 2,000/s, 3 | 8.151708 | 8.560250 | 8.659916 | 통과 |
| 3,000/s, 1 | 7.829333 | 8.392750 | 8.451000 | 통과 |
| 3,000/s, 2 | 7.728792 | 8.050166 | 8.105126 | 통과 |
| 3,000/s, 3 | 7.903875 | 8.131125 | 8.245750 | 통과 |
| 5,000/s, 1 | 9.102792 | 15.148083 | 24.861709 | INVALID_MEASUREMENT·MEASURE_GENERATOR_OR_UNKNOWN·UNSEALED_EXPERIMENT·WINDOW_1_P99 |
| 5,000/s, 2 | 7.296334 | 21.037500 | 25.194000 | WINDOW_1_P99 |
| 5,000/s, 3 | 6.853833 | 6.944041 | 6.990792 | 통과 |

5,000/s 첫 실행은 본 측정의 GENERATOR_LATE 54건으로 measurementValid=false다. 실험은 실패 자료로 보존돼 완료 봉인이 없으며, 예정 시각 기준 창 p99 24.861709ms도 초과했다. 나머지 성공 표본의 전체 요청 p99 9.102792ms만 보고 통과로 취급하지 않는다. 두 번째 실행은 측정 자체는 유효하지만 창 요청/예정 p99 21.037500/25.194000ms로 실패했고, 세 번째만 통과했다.

2,000/s의 고유 신규 승인 처리량은 1,298.05~1,298.10/s, 3,000/s는 1,948.75~1,948.95/s다. 요청에는 중복/거절/취소가 포함되므로 예정 요청률과 고유 승인 주문 처리량을 혼용하지 않는다.

## 누적 판정과 해석

| 예정 요청률 | 이번 반복 통과 | 이전 포함 누적 통과 | 누적 qualified |
| --- | ---: | ---: | --- |
| 2,000/s | 3/3 | 6/6 | true |
| 3,000/s | 3/3 | 5/6 | false |
| 5,000/s | 1/3 | 3/6 | false |
| 10,000/s | 새 측정 없음 | 2/3 | false |
| 20,000/s | 새 측정 없음 | 2/3 | false |

이번 캠페인만의 가장 높은 통과 측정점은 3,000/s지만 4,000/s는 측정하지 않았으므로 인접한 1,000/s 용량 경계를 확정하지 않는다. 이전 실패를 포함하면 3,000/s는 5/6, 5,000/s는 3/6이어서 누적 기준을 채택하지 않는다. 새 통과를 선택해 과거 실패를 삭제하거나 같은 source의 안정적 최대 용량을 증명했다고 주장하지 않는다.

실행 중 호스트 busy 중앙값은 2,000/s 19.5/19/19%, 3,000/s 17/17/16%, 5,000/s 18.5/20/16%였다. 실행 직전의 읽기 전용 프로세스 관측에는 fseventsd·Floorp 등의 CPU 사용이 있었지만, 프로세스 %CPU는 한 코어 기준이고 호스트 busy와 분모가 다르다. 이 시점 관측으로 미전송/창별 지연의 유일한 원인을 확정하지 않는다. 서버/생성기와 다른 작업을 완전히 격리하지 않았으며, 실행 전 유휴 통과도 실행 중 간헐 지연을 제거하지 않는다. 다른 프로세스 종료·OS 설정 변경은 하지 않았다.

생산 코드를 바꾸지 않은 반복 측정이므로 최적화 효과나 이전 결과 대비 회귀/향상 비율을 주장하지 않는다. 이번 범위는 세 요청률 재확인이고 더 높은 rate나 내재/전역 최대 용량, 장시간 지속 성능, 실제 전원 장애는 검증하지 않았다.

## 재현과 검증

다음은 실제 실행 명령이다. 기존 출력 대신 새 경로를 지정한다. 같은 producer는 로컬 보존 빌드이며 현재 source로 새 빌드하면 실제 revision/hash를 다시 기록해야 한다.

```sh
python3 tools/diagnostics/experiment.py baseline --build-dir var/normal-capacity-build-20261107 --output var/normal-capacity-recheck-20261109-2000 --preset reference --normal-rate 2000 --condition normal --quiet-attempts 15
python3 tools/diagnostics/experiment.py baseline --build-dir var/normal-capacity-build-20261107 --output var/normal-capacity-recheck-20261109-3000 --preset reference --normal-rate 3000 --condition normal --quiet-attempts 15
python3 tools/diagnostics/experiment.py baseline --build-dir var/normal-capacity-build-20261107 --output var/normal-capacity-recheck-20261109-5000 --preset reference --normal-rate 5000 --condition normal --quiet-attempts 15
```

rate 사이에도 30초 이상 간격과 시작 gate를 유지한다. 각 root에 verify를 수행했고 기존 frozen benchreport로 신규 9개와 누적 24개를 각각 다시 판정했다. 원본 표본·실패·WAL은 Git 제외 var에, 재집계 report 경로와 hash는 기계 판독 결과에 있다. 이번 변경은 문서뿐이므로 코드 테스트를 반복하지 않고 원본/판정/JSON·링크/경로/공백을 검증한다. 기존 생산/분석 source의 CI와 새 결과 커밋의 CI는 실제 커밋별로 구분한다.

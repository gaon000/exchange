# 전송 오류·UNKNOWN 보고서 실제 검증

2026-11-17, master efe1864 이후 보고서 기능 브랜치에서 기존 10분 끔/켬과 봉인된404 후속 조회, 기존/신규 고정 장애 자료를 분석했다. **당시 UNKNOWN·measurementValid와 후속 조회를 구분했고 404 UNKNOWN은 미확정으로 유지했다.** 새 장시간 부하나 서버 재시도는 실행하지 않았다. 신규 소규모 장애 재현만 실제 loopback/APFS에서 실행했다. 실제 전원 장애 검증은 미완료·보류다.

[자동 생성 JSON](20261117-transport-unknown-report.json), [자동 생성 Markdown 전체 표](20261117-transport-unknown-tables.md), [입력 명세](20261117-transport-unknown-spec.json), [명령·계약](../transport-unknown-report.md)을 함께 읽는다.

## 확인한 결과

| 입력 | 원래 UNKNOWN 시도 | 진단 | 지연 | 후속 조회 | 현재 해석 |
| --- | ---: | --- | --- | --- | --- |
| 최신 10분 끔 | 0 | 전송 오류 없음 | 해당 없음 | 없음 | 기존 measurementValid=false 유지 |
| 최신 10분 켬 | 1 | 단계/유형 미계측 | 4.662542ms | 404 UNKNOWN | **미확정**, 기존 UNKNOWN 논리 명령1·measurementValid=false 유지 |
| 기존 고정 장애 재현 | 3 | headers/eof 2, read/unexpected_eof 1 | 과거 자료 미계측 | 세 키 모두 이후200, 보류 요청의404도 보존 | 최초 UNKNOWN3을 유지하고 후속 RESULT_OBSERVED3을 별도 표시 |
| 신규 고정 장애 재현 | 3 | headers/eof 2, read/unexpected_eof 1 | 각 시도의 monotonic 경과 기록 | 세 키 모두 이후200, 보류 요청의404도 보존 | 최초 UNKNOWN3과 후속 결과를 구분. 성능 측정 아님 |

기존 켬의 대상은 run `2ea8a818-ee3b-4c07-aa82-32d528b2f9a3`, bob/place/soak-v1-o-38457, MEASURE index38457이다. 원래 ns 지연4662542를 그대로 변환했다. 당시 진단 부재를 round_trip/other나 연결 거절로 채우지 않았다. 후속404는 별도 봉인 묶음의 ready runId와 요청 키/인덱스에 연결했고 미접수로 재분류하지 않았다.

최신 10분 두 loadgen은 생성기 미전송으로 failed.json이 남은 무효 측정이다. 상위 묶음의193파일 봉인을 확인하고 현재 Go checker의 전체 plan/sample/summary/진단 raw 재집계를 통과시켰다. 실패 marker와 raw hash도 봉인 목록에 대조했다. 실패 자료를 버리거나 성공 완료로 바꾸지 않았다.

기존 장애 자료는 `var/transport-unknown-20261116`, 신규는 `var/transport-report-fixture-20261117`이다. 새 명령 실행은 고유명령5/체결2·정상 재시작3·WAL-only 비교3회로 PASSED했고, 최초 응답·잔고/예약금·FIFO/전체 상태·WAL과 같은 ID 검사를 유지했다. 지연은 기존 동작의 executor 호출 직전~반환만 추가 계측했다. headers/eof의 두 실패에는 클라이언트 단절 직후의 보류 요청과 저장 완료 뒤의 헤더 유실이 함께 있으므로, 단계/유형만으로 접수 여부를 판단할 수 없다.

신규 지연값은 전체 표의 요청별 행에 기록한다. 샘플3개·통제 장애·동일 프로세스/호스트의 관측이며 평균적인 거래 지연이나 처리량 개선 근거가 아니다. 기존 지연 부재를0으로 채우거나 전후 개선율을 계산하지 않았다.

## 재생성과 검증

```sh
python3 tools/diagnostics/experiment.py transport-report \
  --spec docs/benchmarks/20261117-transport-unknown-spec.json \
  --output var/transport-report-new
```

출력 `var/transport-report-final-20261117`과 별도 `var/transport-report-reproduced-20261117`를 보존한다. 명세의 입력은 로컬 Git 제외 자료이며 새 clone에서 자동 복원되는 것은 아니다. 같은 코드/자료로 생성한 JSON과 Markdown·완료 hash를 비교하고 입력 봉인·raw hash를 재확인한다. 각 입력별 증거 hash와 이번 분석 sourceSHA256은 자동 JSON에 있다.

회귀 검사는 단계/유형·시도/논리 키·phase 구분, 과거 지연/진단 부재, 404/202·후속200과 실험 내 BUSINESS 응답의 시점 분리, 다른 run/요청 키/순번/타이밍·reseal한 의미 변조, 원본·기존 출력·symlink 보호와 Markdown escape를 포함한다. native CI smoke에도 현재 checker로 새 부하와 실제 장애 fixture를 읽는 CLI 생성/검증, 출력 재사용·재해시한 raw 의미 변조 거절을 연결했다. 후속 관측을 기존 성공 건수/실패율에 합산하지 않는다.

최종 로컬 검증은 portable245개·native race285개(명시적 자식 helper skip1), 양쪽 Python111개와 진단 smoke가 통과했다. 보고서 경계8개는 최적화 Python(-O)에서도 통과했다. CI 원본은 `var/ci/portable.GOD6Ey`, `var/ci/native.V2G4C6`에 보존했다. JSON/Markdown/manifest/completed는 새 폴더 재생성 결과와 모두 bytes 일치했고 새 파일 공백·문서 경로도 확인했다. 이번 요청에서 커밋·푸시/실제 GitHub CI는 수행하지 않았다.

## 발행 전 검토 보완

보고서가 읽는 plan.jsonl의 전체 행을 manifest 설정에서 재생성한 계획과 대조하도록 --verify-plan을 추가했다. 단순 재해시로 ID·계정·phase·본문/시각·행 개수/순서를 바꾸면 보고서 생성 전에 거절한다. 기존 두 인자 checker의 출력은 유지하며 새 보고서는 planVerified=true를 기록한다. 변경 후 `var/transport-report-reviewed-20261117`로 네 입력을 다시 검증했다. 모든 기존 counts/attempts/originalMeasurement/unknownCommands/transportGroups와 해석이 이전 결과와 같고, 자동 JSON·Markdown을 최신 결과로 갱신했다. 기존 원본/이전 결과는 보존했다.

검토 보완 뒤 최종 로컬 CI도 portable247/native race287·각 Python111개와 진단/보고서 CLI smoke까지 통과했다(`var/ci/portable.8xfbQy`, `var/ci/native.wFbeWr`). `var/transport-report-reviewed-replay-20261117`로 재생성한 report.json/report.md/manifest.json/completed.json이 검토본과 모두 bytes 일치했다. Go 계획 변조 회귀와 native CLI의 재해시된 phase 변조 거절도 확인했다.

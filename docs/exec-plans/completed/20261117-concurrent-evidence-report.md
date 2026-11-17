# 동시 요청·강제 종료 자료 분석 보고서

상태: Completed
시작일: 2026-11-17
종료일: 2026-11-17

## Purpose / Acceptance

기존 transport-report에 동시 요청 v2와 동시 SIGKILL case를 추가한다. 봉인 자료의 client별 오류/지연·원래 UNKNOWN과 후속 GET/재시도, 실제 WAL 순서·snapshot/tail 경계를 JSON/Markdown으로 읽기 전용 재생성한다. 기존 v1/loadgen 결과·404 미확정 계약을 유지한다. 과거 오류 원문에서 기록되지 않은 단계/유형을 추측하지 않는다.

## Progress

- [x] SIGKILL 변경 검토·커밋/master 병합·푸시. 병합 SHA2f9a760의 GitHub CI44018964216 portable/native success·전체 step/log 확인.
- [x] 두 입력 adapter·봉인/순서/최초 결과/시점 일관성 검증·보고서 표.
- [x] 실제 보관 원본/재현 자료와 변조·경로·재생성 회귀.
- [x] 전체 portable/native CI와 문서·실제 결과.

## Surprises & Discoveries

기존 recoverycheck adapter는 v1 transport-loss만 분석한다. v2 관측은 failureStage/errorType을 포함하지만 SIGKILL 관측은 원문 Error와 elapsedNs만 포함하므로 새 보고서의 단계/유형은 null로 보존한다. SIGKILL 묶음은 독립 run3개이므로 입력 명세에서 case를 명시한다.

## Decision Log

- 2026-11-17: 기존 명령/명세 v1에 concurrent·concurrent-crash 종류를 추가하며 기존 출력 필드는 유지한다. 새 종류의 execution/시점별 표를 덧붙인다. 매칭이나 서버를 재실행하지 않고 자료 간 일관성 검증과 원래 검증 결과를 구분한다.
- 2026-11-17: 새 production dependency·장시간 부하 없음. 전원 장애는 계속 미완료·보류, v1 Blocked 유지. 이번 푸시는 직전 SIGKILL 변경이며 보고서 변경은 별도 기능 브랜치에서 구현한다.

## Outcomes & Retrospective

구현/검증 완료. 실제 원본/replay8입력·고유4묶음276파일을 재검증하고 var/concurrent-report-final-20261117 및 var/concurrent-report-final-reproduced-20261117의 JSON/Markdown bytes가 같았다. 기존 v1·10분4입력도 var/concurrent-report-legacy-recheck-20261117에 분석했고 이전 저장 JSON과 counts/originalMeasurement/attempts/unknownCommands가 그대로였다. 기존 10분 무효 판정과 UNKNOWN1/후속404 미확정은 유지했다.

SIGKILL 자료의24시도는 단계/유형 null로 보존하고 원문 오류를 보고서에 복제하지 않았다. 원래 UNKNOWN·첫 GET·같은 ID retry는 각각 기록했다. 재해시한 양쪽 WAL/live-order·snapshot 기대/실제 변조, recorded 순서·prefix/최초 결과·신호/입력/관측 모순과 경로/출력 실패를 회귀로 확인했다. native smoke가 실제 CI 소유 v2/SIGKILL fixture와 재해시한 snapshot999 모순 거절을 검증했다.

전체 portable CI var/ci/portable.1B2ZjU는 Go255/Python117·smoke exit0, native var/ci/native.pqvvwK는 Go race301(명시적 helper skip1)/Python117·모든 smoke exit0이다. 실행과 필드 정의는 [안내](../../concurrent-evidence-report.md), 원본/재생성 hash와 한계는 [실제 결과](../../benchmarks/concurrent-evidence-report.md)와 [요약 JSON](../../benchmarks/20261117-concurrent-evidence-report.json)에 기록했다.

선행 SIGKILL 변경은 master2f9a760으로 푸시하고 해당 GitHub CI를 확인했다. 최초 구현 완료 시점의 보고서 변경은 codex/concurrent-evidence-report 브랜치에 있었으며 commit/merge/push·새 원격 CI는 수행하지 않았다. 전원 장애는 미완료·보류, v1 전체 계획은 Blocked 유지다. 자료 분석을 독립 매칭/복구 재실행·물리 장애·성능 증거로 확대하지 않는다.

## 발행 검토

2026-11-17 후속 사용자 요청으로 보고서 변경을 검토하고 master에 병합/푸시한다. 기존 v1/loadgen 계약, v2/SIGKILL adapter의 봉인·순서/최초 결과·UNKNOWN/조회/retry 분리와 출력 보호를 재검토했다. 관련 Python14개 회귀와 두 최종 보고서 봉인을 재확인했고 발행을 막는 결함은 발견하지 못했다. 소스가 같은 전체 로컬 CI 근거는 유지하며 병합 SHA의 실제 GitHub CI를 별도로 확인한다.

## Validation and Acceptance

실제 봉인된 동시 요청 원본/replay와 SIGKILL 세 case·원본/replay를 분석하고 JSON/Markdown 재생성 bytes·원본 불변을 확인한다. 과거 v1/10분 자료 회귀를 유지하고 변조/재해시·UNKNOWN/후속 관측·기록 순서·경로·기존 출력 보호를 검사한다. 전체 두 로컬 CI와 직전 푸시 SHA의 실제 GitHub CI를 확인한다.

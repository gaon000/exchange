# Execution Plans

ExecPlan은 대화 이력 없이 작업을 이어갈 수 있도록 목적, 진행 상태, 결정과 검증을 기록하는 실행 계획이다. 큰 작업에만 사용하고 작은 작업은 [Workflow](../workflow.md)에 따라 바로 진행한다.

## 만들기

[템플릿](template.md)을 `active/<작업-ID>-<짧은-이름>.md`로 복사한다. 작업 ID는 날짜·시간과 짧은 이름 또는 기존 이슈 ID를 사용한다. 예를 들어 `20261028-1430-document-search.md`처럼 짓고, active·completed·cancelled 전체에서 중복되는지 확인한다. 실제 생성한 파일만 [active 목록](active/index.md)에 연결한다.

현재 코드 경로, 필요한 계약, 실행 명령, 관찰 가능한 완료 조건을 적는다. 다른 문서로 연결할 수 있지만 작업 재개에 필요한 핵심 맥락과 결정은 본문에도 남긴다. 알 수 없는 사실은 근거와 함께 미확정으로 표시한다.

## 유지하기

다음 네 섹션은 항상 유지한다.

- `Progress`: 완료·진행·남은 작업.
- `Surprises & Discoveries`: 새로 발견한 사실과 근거.
- `Decision Log`: 결정, 이유와 날짜.
- `Outcomes & Retrospective`: 결과, 남은 한계와 후속 작업.

중단 지점마다 현재 상태를 갱신한다. 진행 상태는 Draft, Ready, In Progress, Blocked를 사용하고 active에 둔다. Blocked에는 진행할 수 없는 이유와 필요한 다음 조치를 적는다. 종료 상태는 Completed와 Cancelled를 사용하며 본문의 종료일을 `YYYY-MM-DD`로 기록한다. 여러 계획이 같은 계약이나 파일을 바꾸면 합치거나 선후관계를 명시한다.

## 완료하기

필수 작업과 검증이 완료되어야 상태를 Completed로 바꾸고 completed로 이동한다. 합의된 검증 범위 밖 항목은 결과에 명시한다. 계획의 결과·결정·검증과 종료일을 기록하고 파일명은 유지한 채 이동하며 active와 completed 목록, 상대 링크를 갱신한다. 의도적으로 연기한 항목만 기술 부채에 등록한다.

완료 계획은 당시 기록이다. 이후 구현이 바뀌면 현재 요구사항·설계·구조 문서를 갱신하고 과거 검증을 최신 결과로 재사용하지 않는다.

## 취소하기

취소가 결정되면 상태를 Cancelled로 바꾸고 종료일과 취소 사유를 기록한 뒤 cancelled로 이동한다. `Outcomes & Retrospective`에는 실제 수행한 작업과 검증, 남은 작업, 대체 계획이 있는지를 남긴다. 파일명과 과거 결정·검증 기록을 보존하고 active와 cancelled 목록, 상대 링크를 갱신한다. 취소된 계획은 completed에 넣거나 완료 실적으로 보고하지 않는다.

취소와 일시 차단을 구분한다. 계속 수행할 작업에 필요한 조건을 기다리는 경우에는 Blocked로 active에 유지한다. 취소된 계획을 다시 추진하면 새 ID의 계획을 만들고 양쪽에서 연결해 취소 당시의 기록을 보존한다.

## 목록

- [Active](active/index.md)
- [Completed](completed/index.md)
- [Cancelled](cancelled/index.md)
- [Tech debt](tech-debt-tracker.md)

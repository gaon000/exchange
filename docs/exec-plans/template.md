# {{TASK_TITLE}}

상태: Draft
종료일: 미종료

이 문서는 생성 시 [ExecPlan 운영 규칙](index.md)을 따른다. active에 복사한 뒤 위 링크를 `../index.md`로 고치고 모든 작업별 자리표시자를 채운다. completed 또는 cancelled로 이동할 때는 본문 상태를 Completed 또는 Cancelled로 바꾸고 종료일을 `YYYY-MM-DD`로 기록한다.

## Purpose / Big Picture

{{USER_OUTCOME}}

누가 무엇을 할 수 있게 되는지와 실제로 관찰할 완료 조건을 설명한다. 범위와 제외 범위를 명시한다.

## Progress

- [ ] 현재 코드와 계약 확인.
- [ ] 구현 및 관련 문서 갱신.
- [ ] 검증과 결과 기록.

작업별 단계로 바꾸고 진행 시 날짜와 완료·남은 범위를 기록한다.

## Surprises & Discoveries

발견 사항과 근거를 기록한다. 아직 없으면 없다고 적는다.

## Decision Log

결정, 대안, 이유와 날짜를 기록한다. 주요 미확정 결정이 있으면 Ready로 바꾸지 않는다.

## Outcomes & Retrospective

진행 중에는 진행 중이라고 적는다. 완료 시 달성 결과, 남은 한계와 합의된 후속 작업을 기록한다. 취소 시 취소 사유, 실제 수행한 작업과 검증, 남은 작업과 대체 계획이 있는지를 기록한다.

## Context and Orientation

{{CURRENT_CONTEXT}}

현재 관련 경로, 책임, 계약과 의존 관계를 적는다. 이전 대화를 전제하지 않는다.

## Plan of Work

{{IMPLEMENTATION_STEPS}}

각 단계의 변경 위치, 동작과 검증 방법을 설명한다. 단계별로 확인 가능한 결과를 만든다.

## Validation and Acceptance

{{VALIDATION_COMMANDS_AND_EXPECTATIONS}}

작업 경로, 환경, 명령, 기대 결과와 실제 결과를 구분한다. 실패·재시도·회귀 시나리오와 미실행 이유도 기록한다.

## Idempotence and Recovery

{{RETRY_AND_RECOVERY}}

부분 실패와 재실행에서 중복 변경을 막는 방법, 데이터·배포 영향과 복구 절차를 적는다. 해당 없으면 이유를 적는다.

## Artifacts and Notes

핵심 근거, 관련 코드·문서 링크를 기록한다. 비밀값과 과도한 원본 로그는 남기지 않는다.

## Interfaces and Dependencies

변경되는 공개 계약, 데이터, 의존성과 승인 여부를 적는다. 해당 없으면 해당 없다고 적는다.

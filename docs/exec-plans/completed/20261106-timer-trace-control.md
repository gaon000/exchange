# 타이머 trace 켬·끔 대조 실험

상태: Completed
종료일: 2026-11-06

[운영 규칙](../index.md)을 따른다.

## Purpose / Big Picture

[이전 분리 실험](../../benchmarks/timer-os-io-diagnosis.md)의 trace 비용 가능성을 확인한다. 같은 도구에서 runtime trace·clock anchor·지연 Log를 함께 켜거나 끄며 타이머 단독·2,048 worker 전달 각각 100/s 60,000개를 실행한다. 총 4회 10분 스케줄이며 거래·저장 API 변경은 없다.

## Progress

- [x] 2026-11-06: 현재 feature branch와 기존 미커밋 문서를 확인·보존하고 새 고유 실험 parent 생성.
- [x] 옵션 추가·관련 race 검사·실제 CLI on/off 예비 실행.
- [x] 단독 끔→단독 켬→worker 켬→worker 끔, 각 600초 순차 실행.
- [x] 원본·trace/비활성 검증·분석·보고서·문서 검증.

## Surprises & Discoveries

기존 도구는 trace.Start가 항상 켜지고 Log의 fmt.Sprintf도 실행됐다. 새 옵션은 둘을 함께 비활성화한다. 따라서 trace backend 단독 비용이 아닌 전체 선택 계측의 영향을 비교한다.

## Decision Log

- 2026-11-06: 같은 binary와 100/s·600초·20ms 뒤 epoch·50ms 검사·표본 배열을 유지하고 trace만 선택한다. JSON 표본은 모든 조건에서 종료 후 쓴다.
- 2026-11-06: 각 조건 1회, 순서는 끔/켬/켬/끔으로 정하고 매회 30초 유휴한다. 별도 테스트·재집계와 겹치지 않는다. 호스트 경쟁·온도·캐시는 통제하지 않아 단일 쌍으로 인과를 확정하지 않는다.
- 2026-11-06: 관리자 권한이 필요한 커널 I/O 추적과 실제 전원 장애는 기존 미확보·보류 상태를 유지한다. 커밋·병합·push는 이번 실행에 포함하지 않는다.

## Outcomes & Retrospective

네 조건 각 60,000개, 총 240,000개 예정 표본과 480개 5초 구간을 검증했다. worker trace 끔에서도 전달 전 55.610ms 한도 초과 1건이 발생했다. 선택 trace가 긴 지연의 필수 조건은 아니지만 이번 켬/끔 차이의 인과 효과는 호스트 상태와 순서 영향 때문에 미확정이다. worker 켬에서 UTC−trace 끝점 차이 약 -2.056초도 기록하고 monotonic 지연과 구분했다. [보고서](../../benchmarks/timer-trace-control.md)와 [선택 집계](../../benchmarks/20261106-timer-trace-control-report.json)에 결과를 남겼다. 관리자 커널 추적·실제 전원 장애·새 거래 복구 시험은 수행하지 않았다. 커밋·병합·push는 하지 않았다.

## Validation and Acceptance

타이머 경계·worker 합류·취소 race 테스트, CLI 예비 실행에서 on의 trace 파일·정상 parser와 off의 파일 부재·traceBytes=0을 확인한다. 본 실행 240,000개 표본의 순서·예정 시각·경계·실패 판정을 원본에서 다시 계산한다. on의 표시를 표본 offset과 대조하고 off는 trace 파일을 요구하지 않는다. 새 경로만 사용하고 소유 자식만 제어한다.

## Artifacts and Notes

Git 제외 var/timer-trace-control-20261106.vfehlj86에 binary·도구·환경·입력·trace·표본·분석·manifest를 보존한다. 거래소 실행 코드 변경·새 의존성 없음.

# 스냅샷 단계 계측과 동일 상태 반복 측정

상태: Completed
종료일: 2026-10-30

## Purpose / Big Picture

10분 실험에서 관찰한 생성 지연과 느린 복구를 단계별로 설명한다. 생성·복구 계측을 추가하고 동일 Genesis/WAL/최종 상태의 사본을 반복 실행해 병목과 편차를 기록한다. 최적화나 실제 전원 장애 검증은 이 작업에 포함하지 않는다.

## Progress

- [x] 2026-10-30: 기존 변경 보존, feature branch 생성, 저장·복구 경로 확인.
- [x] 2026-10-30: 생성·복구 단계 계측, byte/실패/동시성·CLI 보고 회귀 검증. portable 187개, native race 219개 통과.
- [x] 2026-10-30: 51,385 명령 상태의 WAL/snapshot 복구 각 5회, 새 파일 생성 5회·동일 파일 재확인 5회. 전체 snapshot bytes와 WAL 불변 확인.
- [x] 2026-10-30: 기존 234개 실험 원본 해시 재검증, 새 원본 manifest·집계·보고 및 계측 계약 기록.

## Surprises & Discoveries

복구는 snapshot decode뿐 아니라 상태 관계 검사, 전체 WAL 대조, canonical bytes 재생성 대조와 strict sync를 수행한다. 기존 복구 전체 시간만으로 원인을 단정할 수 없다. CPU profile의 syscall 비중과 직접 ReadAt 측정이 달라 후자를 3회 재검증했다. 읽기는 전체 Read의 1.1~2.1%였으며 profile sample을 디스크 시간으로 해석하지 않았다. 최초 실험 사본에 .lock을 빠뜨린 준비 실패는 보존하고 완전한 새 사본으로 재실행했다. 기존 분석용 Go 파일의 ./... 포함은 ignored var/go.mod 경계로 해결해 원본을 유지했다.

## Decision Log

- 2026-10-30: 거래·저장 포맷과 검증 순서를 유지하고 단계 경계에서만 시간을 잰다. 명령별 타이머는 추가하지 않는다.
- 2026-10-30: 기존 10분 snapshot 조건의 최종 상태 하나를 공통 기준으로 사용한다. 각 실행은 새로운 사본과 프로세스를 쓰고, WAL-only와 snapshot 복구의 순서를 교차한다. 캐시를 강제로 비우지 않으며 cold-start로 표현하지 않는다.

## Outcomes & Retrospective

복구 중앙값 WAL 1.134초, snapshot 3.934초. snapshot decode 2.232초(주로 results·orders), 재확인 0.514초. 새 파일 생성 0.603초 중 길이 산출/직렬화·쓰기가 약 90.7%. owner capture 43.061ms는 별도 지연 개선 과제다. 기존 실험/전원 장애 보류를 유지하며 최적화·commit·merge·push는 하지 않았다.

## Context and Orientation

[이전 실험](../../benchmarks/soak-10m-snapshot-comparison.md)은 두 조건의 최종 순번이 달랐다. 이번에는 snapshot 조건의 순번 51,385와 동일한 WAL을 두 복구 경로에 사용한다. 계측은 internal/telemetry, engine/checkpoint, snapshot, runtime과 CLI ready 보고에 연결한다.

## Plan of Work

생성의 copy/sort, payload 크기 산출/쓰기, sync/rename과 복구의 WAL open, checksum/decode, 엔진 재구성/검사, WAL 대조, snapshot 재확인, WAL sync를 분리한다. 중첩 시간을 합산하지 않도록 계약을 기록한다. 같은 상태의 사본으로 반복하고 복구 결과·WAL·새 snapshot bytes를 검증한다.

## Validation and Acceptance

계측 분리·실패·복사/동시성, 기존 저장/복구/HTTP 회귀 테스트와 portable/native CI 스크립트를 실행한다. 최소 5회씩 같은 입력의 복구와 새 snapshot 생성을 측정하고 중앙값·범위, 상태 동일성, 환경과 원본 hash를 보고한다. 로컬 portable 187개/18패키지와 native race 219개/18패키지(의도한 helper skip 1개)가 통과했다. profiler 별도 1회와 읽기 호출 진단 3회는 반복 통계에서 제외했다. 10회 snapshot bytes·WAL·Genesis 일치와 원본 보존을 재검증했다. 측정 이후 실패한 WAL 재생의 후속 미실행 단계를 제외하는 계수 보완과 회귀 검증을 추가했고 전체 CI를 재통과했다. 측정/최종 소스 archive를 분리했다. 원격 CI는 실행하지 않았다.

## Idempotence and Recovery

기존 run은 읽기만 한다. 새 고유 실험 디렉터리에 사본을 만들며 기존 디렉터리 재사용을 거절한다. 종료하는 프로세스는 실험 소유 자식뿐이다. 실패 결과도 보존하고 원본을 삭제하지 않는다.

## Artifacts and Notes

[단계 측정 보고서](../../benchmarks/snapshot-stage-breakdown.md), [재집계](../../benchmarks/20261030-snapshot-stages.json), [manifest 요약](../../benchmarks/20261030-snapshot-stage-artifacts.json)에 원본과 한계를 기록했다. 커밋·병합·push는 요청되지 않았다.

## Interfaces and Dependencies

CLI ready.recovery의 단계 보고와 runtime statistics의 snapshotStages를 추가한다. 정수 시간은 nanoseconds 문자열이며 시도 횟수는 성공 건수와 다르다. 기존 snapshot/WAL bytes와 거래 API 응답은 유지한다. 외부 dependency는 추가하지 않는다.

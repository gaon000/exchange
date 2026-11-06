# 타이머 분리 재현과 저장 호출 OS I/O 관찰

상태: Completed
종료일: 2026-11-03

[운영 규칙](../index.md)을 따른다.

## Purpose / Big Picture

이전 trace의 timer/select 대기와 긴 저장 호출을 분리해 조사한다. 타이머만 100/s 600초, 동일 타이머와 2,048 worker handoff만 600초를 순차 실행한다. 거래소 snapshot 조건은 동일 입력 600초로 실제 저장 호출과 프로세스별 OS I/O 누계를 수집하고 전체 복구를 확인한다. production source와 의존성은 바꾸지 않는다.

## Progress

- [x] 기존 미커밋 문서·원본을 보존하고 현재 feature branch 재사용.
- [x] fs_usage 관리자 권한 필요·현재 개발 도구 설정에서 Instruments 사용 불가 확인.
- [x] task-local 타이머 도구·OS I/O sampler·C 호출 계측 구현과 예비 검증.
- [x] 순차 본 실험·원본·복구 검증.
- [x] 분석·한계·보고서·문서 검증.

## Surprises & Discoveries

sudo 비대화 실행은 password required였고 xcrun xctrace도 사용할 수 없다. 커널 이벤트 추적은 확보하지 못한다. OS proc_pid_rusage 누계와 실제 F_FULLFSYNC의 C 구간 벽시계/호출 스레드 CPU를 대조하는 제한된 관찰을 수행한다. 보안 설정·OS 전원은 변경하지 않는다.

## Decision Log

- 2026-11-03: 순수 타이머와 worker 전달을 구분하고 HTTP/엔진/저장 작업 없이 같은 100/s·50ms 지연 판정을 사용한다. trace·출력 비용을 조건에 기록한다.
- 2026-11-03: syscall 내부·장치 지연을 증명하지 못한 부분은 미완료로 명시한다. 가능한 실험은 계속 수행하며 실제 전원 장애는 보류다.

## Outcomes & Retrospective

타이머 단독·worker 전달 각 60,000개와 snapshot 거래 600초 실행을 완료했다. worker 예정 대비 55.974ms 한도 초과 1건은 HTTP/엔진/저장 없이 재현됐으나 trace 계측 영향과 전달 전 지연을 배제하지 못했다. WAL C 구간 최대 39.639ms·스레드 CPU 0.036ms, OS 누계 3,022개를 확인했다. 실제 거래 미전송 2건은 무효 성능 측정으로 보존했고 전체 복구·12개 재시도·WAL 불변은 일치했다. [보고서](../../benchmarks/timer-os-io-diagnosis.md)에 결과·원본·검증·한계를 기록했다. 가능한 OS 누계 관찰은 완료했고, 관리자 권한이 필요한 커널 이벤트 추적은 미확보다. 커밋·병합·push는 하지 않았다.

## Validation and Acceptance

도구 테스트·짧은 예비 실행과 실제 OS counter 읽기를 확인한다. 본 실험은 순차 실행하며 원본과 script hash를 보존한다. trade 실행은 모든 최초 결과·주문·잔고·체결·재시도·WAL 일치를 확인한다. unavailable과 측정된 0을 구분한다. 새 parent만 사용하고 소유 프로세스만 제어한다.

## Artifacts and Notes

Git 제외 var/timer-io-20261103.01lq38i1에 도구·입력·trace·OS 누계·원본·분석·해시를 남긴다. Git에는 보고서·집계·계획만 기록한다.

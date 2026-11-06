# 실행 trace와 호스트 자원 진단

상태: Completed
종료일: 2026-11-03

[운영 규칙](../index.md)을 따른다.

## Purpose / Big Picture

[반복 측정](../../benchmarks/diagnostic-soak-repeats.md)의 생성기 실행 지연과 서버 저장 지연을 별도 trace 실행에서 관찰한다. 끔/켬 각 600초, 같은 요청 계획·500ms 진단을 사용하고 CPU/I/O를 1초 간격으로 읽는다. 실제 전원 장애는 보류다.

## Progress

- [x] 기존 사용자 미커밋 문서를 보존하고 기존 feature branch를 재사용했다.
- [x] task-local Go overlay와 관련 race 테스트·짧은 실제 예비 실행.
- [x] 끔/켬 각 600초, 전체 복구와 trace/호스트 자료 검증.
- [x] trace 분석·근거·한계와 보고서 기록.

## Surprises & Discoveries

기존 CLI에는 execution trace 기능이 없어 task-local overlay로 main 수명·생성기 10ms 초과 경계·WAL sync region만 관찰한다. iostat 읽기는 sandbox 밖 권한이 필요하다.

## Decision Log

- 2026-11-03: production source·dependency를 바꾸지 않고 overlay·binary·입력 hash를 보존한다. trace는 파일당 512MiB, 기존 디스크/RSS guard를 적용한다.
- 2026-11-03: trace·호스트 관찰 비용이 있는 별도 진단이며 무진단 성능 개선·최대 처리량을 주장하지 않는다. 재현되지 않은 문제도 그대로 기록한다.

## Outcomes & Retrospective

끔/켬 각 600초, 전체 복구·최초 결과·12개 중복 재시도·WAL 일치. 50ms 미전송은 재현되지 않았다. 10~40ms 지연에서 timer/select와 runnable 대기를 구분했고 최대 WAL sync 505.110ms 중 Syscall 상태 약 505.046ms를 확인했다. [보고서](../../benchmarks/trace-host-diagnosis.md)에 근거와 한계를 기록했다. overlay 네 패키지 race 및 실제 HTTP/APFS·6초 예비 실행, 네 trace 전체 parser·profile 변환, marker/index/offset 원본 대조, archive 모든 member 재검증을 통과했다. 기존 반복 실행 문서는 당시 결과로 유지한다. 커밋·병합·push는 하지 않았다.

## Validation and Acceptance

관련 기존 race·HTTP/APFS 테스트, 6초 예비 실행, 본 실행의 모든 최초 결과·상태·재시도·WAL 일치 검증을 수행한다. Go trace parser와 profile 해석, 500ms 진단·1초 호스트 시간 정렬의 한계를 기록한다. 새 parent만 쓰며 소유 자식만 제어한다.

## Artifacts and Notes

Git 제외 var/trace-diagnosis-20261103.xu6tpijk에 overlay·원본·해시를 보존한다. 보고서와 선택 집계만 Git에 남긴다.

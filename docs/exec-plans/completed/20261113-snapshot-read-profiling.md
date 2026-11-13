# 최신 스냅샷 읽기 경로 CPU·할당 분석

상태: Completed
종료일: 2026-11-13

## Purpose / Big Picture

master 27b4621의 생산 코드를 유지하고 최근 snapshot-tail 실험의 동일 active/history 각 1만 명령 최종 상태를 사용한다. 공개 Open의 CPU 각 20회·allocation 각 5회를 별도 새 프로세스로 수집하고 read/decode/restore/confirm/WAL 호출 경로와 기존 세부 stage를 분리해 다음 최적화 후보를 정한다. tail=0이며 이후 명령 수에 따른 성능 비교나 최적화 구현은 이번 범위가 아니다.

## Progress

- [x] 최신 master/입력/계약/공간 확인·feature branch 생성.
- [x] 기존 Go profile probe를 재사용하는 전용 조정기·집계/회귀 구현.
- [x] 현재 도구 smoke·전체 CI 확인 후 동결한 빌드로 50 worker 수집.
- [x] source/fixture/전체 bytes·재집계 검증과 JSON/Markdown·후속 후보 문서화.

## Decision Log

- 2026-11-13: 기존 상태 비용 fixture는 WAL/snapshot 최적화 이전 source 제약이 있으므로 최근 tail 실험의 동일 최종 snapshot/WAL을 사용한다. 기존 Go 공개 Open profile 진입점을 그대로 재사용하고 Python 도구만 추가한다.
- 2026-11-13: CPU 기본 10ms sample과 allocation 64KiB sample/baseline 차이를 별도 프로세스에서 수집한다. profile 수집/전역 background와 내부 호출 경로, 자연 GC/I/O 대기·wall stage를 구분한다. focus는 중첩되므로 합하지 않는다.
- 2026-11-13: warm-cache·비격리 호스트 관찰이며 실제 전원 장애/HTTP p99 검증은 포함하지 않는다. 4GiB 묶음 상한/별도 8GiB 여유와 입력 원본·실패 보존을 유지한다.

## Validation and Acceptance

정확한 snapshot 순번/재생 수 0·fallback 부재·전체 canonical snapshot/Genesis/WAL/lock 불변, 프로파일별 표본 설정/정확한 반복 수·불완전/변조/부족 공간 거절, 동결 source/binary·기존 자료 보존과 새 폴더 재집계를 확인한다. source 변경 뒤 Python 회귀와 portable/native CI를 실행한다. 결과/도구 문서와 완료 계획을 연결한다.

## Outcomes & Retrospective

50개 worker가 동일한 두 최종 상태와 정확한 snapshot/재생 경로·전체 bytes 불변을 통과했다. 읽기 CPU 표본/호출은 active 147.5ms·history 126.5ms, 읽기 추정 할당/호출은 42.652/33.268MiB다. pread stack 자체가 read CPU의 68.81%/66.40%, parseArray 자체가 read 할당의 40.86%/38.00%, canonical Marshal 누적은 36.95%/37.88%였다. 중첩 비중을 합하거나 I/O 대기·이전 wall 개선으로 해석하지 않는다.

다음 첫 후보는 decoder 읽기 버퍼 크기·실제 pread 요청 수 대조, 다음은 배열 증가/Marshal 반환 bytes 할당 감소다. 이번에는 생산 최적화를 구현하지 않았다. [결과·조건·후속 검증](../../benchmarks/snapshot-read-profiling.md), [명령·해석](../../snapshot-read-profiling.md)을 따른다.

Go source는 그대로이고 Python 전용 조정기/회귀를 추가했다. 작은 CPU 표본 0의 peek 실패를 보존/수정하고 새 4 worker smoke 통과, 최종 portable 232/native race 270·각 Python 77과 진단 smoke를 통과했다. 50 worker/642개 파일·247개 code archive/현재 source·입력 314개 파일을 확인했고 새 JSON/Markdown 재집계 bytes도 같다. 4GiB 상한/8GiB 여유·원본을 유지했다.

병합 전 검토에서도 코드 변경 없이 동결 source/측정 원본·전체 Python 77개·새 재집계 JSON/Markdown bytes 일치를 확인했다. 위 기록은 수집·분석/검토 시점의 로컬 근거이며 후속 커밋·병합·푸시·해당 master의 원격 CI는 별도로 확인한다. v1 전체의 전원 장애 필수 검증은 미완료·보류이고 Blocked를 유지한다.

# 최적화 후 CPU·할당 프로파일 갱신

상태: Completed
종료일: 2026-11-13
시작일: 2026-11-13
기준: RSS 진단 커밋 dc7f10f (생산 decoder는 master 24bc995와 동일)

## Purpose / Acceptance

RSS 진단 커밋 뒤 앞서 정한 다음 작업인 최신 CPU/할당 병목을 갱신한다. 같은 active/history 각1만 명령 최종 상태(tail=0)에서 CPU 각20회·allocation 각5회, 총50개 새 worker로 수집한다. CPU와 allocation은 별도 프로세스이며 Go profile probe/생산 코드를 수정하지 않는다. RSS 진단의 시간별 관측·추가 GC/FreeOSMemory를 profile 경로에 연결하지 않는다.

현재 source가 입력의 최신 producer와 같은지 검사하면서 기존 tail·정규형 버퍼·RSS 묶음을 안전하게 재사용하도록 profile 조정기의 입력 검증을 확장한다. format/메모리·공간/실패·원본 보존/전체 bytes 검증을 유지한다. 새 profile로 남은 병목과 다음 후보를 정리하되 이번에 추가 최적화는 구현하지 않는다.

## Progress

- [x] RSS 진단 변경 커밋 dc7f10f, 후속 기능 브랜치 생성.
- [x] 입력 종류별 원본/producer 검증·회귀, smoke/동결 빌드 준비.
- [x] CPU40/alloc10 worker·모든 상태/파일/복구 경로 검증, 원 자료 분석.
- [x] JSON/Markdown·새 폴더 재집계·로컬 CI·다음 후보 문서화.

## Surprises & Discoveries

- 기존 read-profile 조정기는 오래된 snapshot-tail producer만 허용해 현재 decoder와 같지 않다. 새 RSS/정규형 비교 묶음의 candidate 생산 source를 기준으로 검증해야 한다.
- 최초 smoke는 cmd/internal의 .DS_Store에 생산 파일 집합 검사가 실패했다. 두 파일을 var에 원래 경로와 함께 보존하고 새 smoke4 worker가 통과했다.
- 이전 Marshal 약44%/Clone 약30%는 정규형 버퍼 최적화 전 allocation 비중이다. 최적화 후의 실제 비중으로 쓰지 않는다.

## Decision Log

- 2026-11-13: 기존 Go profile 명령과 collect/summarize를 유지하고 입력 종류 판정/검증만 확장한다. CPU sample·추정 할당/정확한 TotalAlloc·stage wall을 구분하고 중첩 focus를 더하지 않는다.
- 2026-11-13: 원본/실패 보존, 4GiB 출력/별도8GiB 여유·128MiB 준비 gate를 유지한다. 실제 전원 장애는 미완료·보류다.

## Outcomes & Retrospective

50 worker와642개 봉인 파일·270 source archive member·현재 source/입력 candidate를 검증했다. 원본 JSON/Markdown은 새 폴더 재집계와 bytes까지 일치했다. 최신 보고서는 [분석](../../benchmarks/post-optimization-profiling.md)에 정리했다. 읽기에서 배열 약39%·정수 Marshal 약20%/Unmarshal 약13%·item 임시 객체 약13~14%가 남아 정수 decode 임시 string을 다음 첫 후보로 정했다. 생산 변경이나 profile 간 개선율을 주장하지 않는다.

portable240개/23packages·native race279개/23packages(기존 child-helper skip1개), 각 Python100개·진단/HTTP/복구 smoke가 통과했다. `var/ci/portable.trboN3`·`var/ci/native.zirjWm`에 보존했다. 첫 smoke의 .DS_Store 거절·메타데이터 원본은 보존하고 새 출력에서 재실행했다.

앞선 RSS 변경은 dc7f10f로 로컬 커밋했다. 구현·로컬 검증 완료 시점에는 후속 profile 변경이 기능 브랜치의 작업 트리였다. 이후 커밋/푸시·원격 CI는 별도 발행 단계에서 확인한다. 실제 전원 장애 미완료·보류, v1 전체 Blocked 유지.

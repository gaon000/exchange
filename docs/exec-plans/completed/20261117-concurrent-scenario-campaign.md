# 동시 시나리오 다양화·반복 실행

상태: Completed
시작일: 2026-11-17
종료일: 2026-11-17

## Purpose / Acceptance

기존 v1/v2 재현 계약을 유지하고 v3에 주문 생성/취소 경쟁·같은 ID 집중 중복·기존 주문/취소 ID의 다른 내용 충돌을 추가한다. 제한된 seed/반복 조합을 독립 run에서 순차 실행하고 시간/자료 예산·실패 원본/한 명령 재현·JSON/Markdown 실행표를 제공한다. 실제 WAL 순서·잔고/체결/최초 결과·같은 ID 중복 방지를 검사하며 클라이언트 완료 순서를 저장 순서로 사용하지 않는다.

## Progress

- [x] 선행 보고서 변경 검토·masterf17d5c7 푸시,GitHub CI44023735396 portable/native success·로그 확인.
- [x] v3 입력/가변 클라이언트·충돌 관측·WAL/최초 결과 검증.
- [x] bounded 반복 CLI·시간/용량 예산·실패 보존·재현/집계.
- [x] v3 보고서 adapter/기존 자료·짧은 CI 회귀.
- [x] 실제 캠페인/기록 replay·전체 CI·문서/근거.

## Surprises & Discoveries

기존 v2는 phase당4개와 모든 upstream200을 요구한다. 충돌 요청은 이미 저장된 키의 다른 내용에만 허용해 승자를 임의로 정하지 않고409/NOT_ADMITTED와 WAL/최초 응답 불변을 확인한다. v2 입력은 기존4개 규칙을 유지한다.

초기 portable 전체 CI var/ci/portable.ZUAx0u에서 캠페인 공용 테스트가 native-only failedWriter를 참조해 vet가 실패했다. 전용 공용 campaignFailedWriter로 수정하고 관련 portable/native 회귀와 두 최종 전체 CI를 다시 실행했다. 첫 실패와 먼저 통과한 native.VC9mjr도 보존했다. 파일 용량 감시는 정상 snapshot rename 중 사라진 경로를 건너뛰며, 실패 원본을 삭제해 예산 준수로 위장하지 않는다.

## Decision Log

- 2026-11-17: 새 profile은 v3로 구분하고 phase4~12개를 허용한다. 기존 completed 키에 대한 expectConflict는 원래 canonical 명령을 덮어쓰거나 새 WAL 입력으로 세지 않는다.
- 2026-11-17: 각 반복은 새 거래 저장소이고 실패/관측 순서를 보존한다. 기본은3profile×2seed×2회, 최대24실행·최대5분·자료 예산 제한이다. 성능 부하/전원 장애 검증이 아니다.
- 2026-11-17: 선행 보고서만 발행하고 새 작업은 codex/concurrent-scenario-campaign에서 구현한다. 전원 장애 미완료·보류, v1 전체 Blocked 유지. 새 production dependency 없음.

## Outcomes & Retrospective

구현/검증 완료. var/concurrent-campaign-final-20261117에서 기본3profile×2seed×2회·12독립 run이 모두 PASSED다. top-level700파일의 봉인/실제 bytes와64MiB 예산 내 최종8,909,932 bytes를 확인했다. 원래 UNKNOWN72·의도한409충돌64·같은 ID 중복224를 분리하고 고유 WAL 명령152개를 각 독립 상태에서 검증했다. profile당 첫 실행의 recorded replay를 새 폴더3개에 실행해 전체 checkpoint·각 wave WAL/snapshot 상태가 runId 정규화 뒤 같고 최종 recorded 명세 bytes도 같았다.

12입력 분석표를 var/concurrent-campaign-report-verified-20261117와 verified-reproduced 폴더에 생성해 JSON/Markdown bytes 일치를 확인했다. 기존 v2/SIGKILL8입력과 v1/과거10분4입력은 이전 보고서와 name/counts/originalMeasurement/attempts/unknownCommands가 같았다. 원래 UNKNOWN/미확정과 무효 측정은 유지했다.

최종 portable var/ci/portable.AGxWmP는 Go259/Python118·모든 smoke exit0, native var/ci/native.IHoMnt는 Go race306(명시적 helper skip1)/Python118·기존/새 모든 smoke exit0이다. native 회귀가 실제3profile·기록 replay와 첫 wave의 두 취소 저장 순서를 검사하고, 새 보고서 smoke가 CI 소유 실제 v3 fixture의12클라이언트/16의도한충돌·UNKNOWN 분리를 확인했다. 합성 예산 회귀는 실제 거래 검증과 구분했다.

명령/예산과 재현 범위는 [안내](../../concurrent-campaign.md), 실제 증거/실패/한계는 [결과](../../benchmarks/concurrent-campaign.md)와 [JSON](../../benchmarks/20261117-concurrent-campaign.json)에 기록했다. 원래 v1 전체 계획은 전원 장애 증거가 없어 Blocked 유지다. 새 캠페인은 정상 재시작이며 전원 장애·SIGKILL·성능/독립 매칭 oracle 증거로 확대하지 않는다. 캠페인 변경은 별도 기능 브랜치에서 검증했다. 후속 발행 요청에 따라 자료 보존 범위 변경과 함께 검토해 커밋·master 병합·push 대상으로 묶는다. 원격 상태와 CI 결과는 해당 Git/Actions 이력으로 확인한다.

## Validation and Acceptance

profile/seed/예산·명세 변조 portable 회귀, 실제3profile·기록 순서 replay·실패/취소 및 예산 중단 보존 native 회귀, 기존 v1/v2/보고서 계약과 전체 두 CI를 확인한다. 실제 기본 캠페인 원본과 새 폴더 재현의 논리 상태/순서·봉인을 대조한다. 시간·용량 중단은 성공으로 표시하지 않는다.

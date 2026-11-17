# 동시 요청·장애 조합 통합 검증

상태: Completed
시작일: 2026-11-17
종료일: 2026-11-17

## Purpose / Acceptance

master5558893의 순차 seed/고정 장애 검사에 4개 HTTP 클라이언트의 동시 주문·취소·같은 ID 재시도를 조합한다. 초기 요청의 응답 헤더 유실·본문 절단·원래 요청 전달 보류, 응답 대기 중 snapshot, retry phase·늦은 원래 요청·정상 재시작을 두 wave로 검사한다. 실제 WAL 입력/순번으로 모델을 재생하고 잔고·공급량·호가/FIFO·주문·체결·최초 응답/중복 기록 방지를 대조한다.

저장 명령과 snapshot 경계를 기록한 replay 명세를 새 출력에 보존하고 한 명령으로 재현한다. seed만으로 OS 순서가 같다고 주장하지 않는다. 관측 순서를 얻기 전의 취소/환경 실패는 입력·gate 계획 재실행과 중간 증거를 보존한다. 전원 장애는 미완료·보류, v1 Blocked 유지. 새 production dependency·장시간 성능 측정은 범위 밖이다. 구현 후 별도 사용자 요청으로 검토·commit/merge/push와 GitHub CI 확인까지 진행한다.

## Progress

- [x] 현재 코드/범위·실행 절차 확인, 기능 브랜치 생성.
- [x] bounded v2 동시 명세/seed/기록 순서 gate·입력 검증.
- [x] 실제 중계·동시 요청·snapshot·WAL 순서 oracle·정상/WAL-only 복구·실패 보존.
- [x] seed/기록 replay·변조/취소/중복 회귀·한 명령 실측.
- [x] portable/native 전체 CI·명령/한계/결과 문서.

## Surprises & Discoveries

기존 session의 JSON encoder는 병행 호출용이 아니다. 동시 요청 관측은 각 소유 goroutine에 모아 합류한 뒤 기록하고, step은 wave 동안 고정한다. 기존 순차 모델은 제출 순서대로 Apply하므로 새 모드는 실제 저장 입력을 검증하고 해당 순서로 재생해야 한다. WAL은 소유 runtime 종료 뒤 잠금을 얻어 읽는다.

재시작 전 live HTTP 검사도 추가했다. upstream 결과의 순번으로 만든 임시 모델과 잔고/현재 주문/호가/체결/최초 응답을 대조하고, 이후 실제 WAL 입력/순서와 완전히 같아야 그 검사를 채택한다. FIFO는 동시/복구 checkpoint에서 확인한다.

## Decision Log

- 2026-11-17: 기존 schema v1과 seed/transport-loss는 유지하고 --concurrent --seed를 schema v2로 추가한다. 초기/재시도 각4개·두 wave이며 명령/controls 전체 budget256을 넘기지 않는다.
- 2026-11-17: 서버 저장 hook은 넣지 않고 실제 loopback 중계 gate를 사용한다. snapshot은 선택 응답을 보류한 상태에서 실행한다. held-original의404는 미확정이며 같은 ID retry 뒤 늦은 원래 요청도 최초 결과를 반환해야 한다.
- 2026-11-17: 실제 WAL 순서와 snapshot offset을 저장한 replay 명세는 다음 실행의 admission gate에 사용한다. 검증 엔진은 기존 매칭 구현이며 독립 매칭 모델이라고 주장하지 않는다.

## Outcomes & Retrospective

구현/검증을 완료했다. 최종 사용자 명령 `sh scripts/random-recovery.sh --concurrent --seed 17 --output var/concurrent-fault-final-20261117` 및 `--replay var/concurrent-fault-final-20261117/replay-005.json --output var/concurrent-fault-final-replay-20261117`가 PASSED다. 초기 한 명령/초기 replay 자료도 별도로 보존한다.

최종 실측은4클라이언트/두 wave, 고유명령12/현재 주문10/관측 체결3, phase 시도16/명시적 중복8, client 유실/UNKNOWN6·404 UNKNOWN2, 정상 재시작/WAL-only 각3회다. 첫 경계3+tail3, 두 번째9+tail3, 마지막12+tail0이 두 실행에서 일치했다. 각54개 봉인 파일의 크기/hash와 manifest/completed를 확인했다. runId·tradeId run prefix를 정규화한 전체 최종 상태와 실제 WAL 순서가 같았고 최종 replay-005.json도 bytes 동일했다. live 검사 순서와 실제 WAL, 동시 snapshot의 전체 prefix 상태 및 복구 후 FIFO/잔고/공급량·주문·체결·최초 응답 GET/POST/WAL 불변을 대조했다.

회귀/CI:

- native seed17/42/701 각 실제 동시 실행 및 recorded replay, 독립 명령/주문 수·최소 체결 확인. 저장 순서/snapshot/최종 논리 상태와 최초 응답 유지.
- 출력 실패 후 failed.json이 기록된 순서의 한 명령 재현을 제공하고 새 폴더에서 재실행 성공. 미완료 성공 표식 없음, 원본 명세 hash 유지.
- 첫 신규 명령 뒤 snapshot을 강제하는 경계에서3+tail3/9+tail3를 확인해 나머지 초기 명령이 gate를 넘지 못함을 검증.
- portable 입력/예산·키/내용·client·기록 순서·snapshot gate·실제 중계 취소/합류·부분 replay 무시·저장 미지원 v1/v2 무변경 거절.
- `scripts/go test -race ./tools/recoverycheck -count=1 -timeout=180s` 및 보완 후 관련 race 회귀 통과.
- `bash scripts/ci.sh portable`: var/ci/portable.k1ss8Q,252개 Go/111개 Python·format/vet/build·진단 smoke exit0.
- `bash scripts/ci.sh native`: var/ci/native.xoBAal,295개 Go race(명시적 자식 helper skip1)/111개 Python·기존 보고서/진단 smoke exit0. CI fixture는 기존 random-recovery-* artifact 경로에 포함된다.
- gofmt·git diff --check·새 파일 공백/문서 경로와 실제 자료 봉인 검증 통과.

실제 결과는 docs/benchmarks/concurrent-recovery.md와 20261117-concurrent-recovery.json, 명령/경계·관측 순서 확보 전 실패의 재현 한계는 docs/concurrent-recovery.md에 정리했다. 기존 v1 생성/transport-loss와 보고서 검증도 유지했다. 새v2는 자체 summary/observations/WAL/state 증거를 사용하며 기존 v1 전송 보고서와 섞지 않는다.

실제 OS·전원 장애는 미완료·보류이고 v1 전체 계획은 Blocked다. 성능/최대 처리량·독립 매칭 oracle·SIGKILL 증거를 새로 주장하지 않는다. 최초 구현 완료 시점에는 커밋/merge/push·새 GitHub CI를 수행하지 않았다.

## 2026-11-17 발행 검토

사용자가 변경 검토·커밋·master 병합/푸시와 실제 GitHub CI 확인을 요청했다. 검토 시작 시 작업 기준과 원격 master는 모두5558893이었다. 동시 요청의 합류/취소, 실제 WAL 순서와 최초 응답 대조, snapshot 경계, 기록 순서 replay 및 실패 파일 보존을 검토했고 발행을 막는 결함은 발견하지 못했다. 최종 원본/재현 실행 각54개 파일의 크기/hash·봉인, 논리 상태·WAL 순서 일치도 재확인했다.

소스 변경 없이 관련 회귀를 재실행했다. `scripts/go test -race ./tools/recoverycheck -count=1 -timeout=180s`와 `CGO_ENABLED=0 scripts/go test ./tools/recoverycheck -count=1 -timeout=60s`가 통과했다. 로그는 var/concurrent-review-20261117에 보존했다. portable 최초 재실행은 sandbox의 loopback 포트 제한으로 실패했으며 해당 로그도 보존하고 포트 사용이 허용된 실행에서 통과했다. 위 전체 로컬 CI 근거는 소스가 동일하므로 유지한다. 병합 후에는 정확한 master 병합 SHA의 GitHub CI 결과를 별도로 확인한다.

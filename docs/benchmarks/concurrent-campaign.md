# 동시 시나리오 캠페인 실제 검증

2026-11-17, masterf17d5c7 이후 기능 브랜치에서 세 v3 profile의 기본 캠페인12회와 구성별 관측 순서 replay3회를 통과했다. 실제 HTTP/WAL/snapshot·정상 재시작·최초 결과/중복 방지를 검사하고 원래 UNKNOWN·의도한 내용 충돌을 구분했다. 실제 OS·전원 장애는 계속 미완료·보류다. 이번 캠페인은 SIGKILL이나 처리량/지연 성능 측정이 아니다.

[명령·예산·계약](../concurrent-campaign.md), [검증 JSON](20261117-concurrent-campaign.json)을 함께 읽는다. 실행 코드는 기준f17d5c7에서 modified=true였고 각 case의 build.json에 실제 Go/플랫폼·바이너리 hash가 있다. 원본·초기 실행/실패 로그는 Git 제외 var에 보존했다.

## 실행과 관측 결과

```sh
sh scripts/random-recovery.sh --campaign --seed 17 --output var/concurrent-campaign-final-20261117
python3 tools/diagnostics/experiment.py transport-report --spec var/concurrent-campaign-final-20261117/transport-spec.json --output var/concurrent-campaign-report-verified-20261117
python3 tools/diagnostics/experiment.py transport-report --spec var/concurrent-campaign-final-20261117/transport-spec.json --output var/concurrent-campaign-report-verified-reproduced-20261117
```

출력은 이미 존재하므로 재실행 시 새 이름을 사용한다. 기본은 seed17/18 × 반복1/2 × 세 profile이다.12개 run은 전부 독립 초기 잔고/runId이며 좋은 결과만 골라 재시도한 캠페인이 아니다. top-level manifest의700파일·모든 하위 run 봉인/bytes를 검증했다. 봉인 포함 논리 파일 크기는8,909,932 bytes로64MiB 예산 안이었다. summary가 기록한 metadata 전 관측8,782,023 bytes와 최종 크기를 구분한다. 캠페인 내부 경과 약9.45초는 실행기 시간 예산 관측이며 서버 처리량 수치가 아니다.

| profile | 클라이언트/phase | 독립 run | run당 고유 WAL 명령 | run당 의도한 충돌 | run당 같은 ID 중복 | run당 원래 UNKNOWN |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| cancel-race | 4 | 4 | 12 | 0 | 8 | 6 |
| duplicate-storm | 12 | 4 | 12 | 0 | 40 | 6 |
| id-conflict | 8 | 4 | 14 | 16 | 8 | 6 |

같은 ID 중복은 양 phase 전체 시도에서 새로운 고유 명령과 의도한 충돌을 뺀 값이다. 전체12run의 고유 저장 명령152개, 중복224회, 의도한409충돌64회와 원래 UNKNOWN72회를 각 결과에 그대로 기록했다. 독립 실행·업무 거절/중복을 합쳐 처리량이나 성공률을 계산하지 않았다.

취소가 신규 대기 주문보다 먼저면 저장된 ORDER_NOT_FOUND, 뒤면 당시 주문 상태에 맞는 결과를 반환한다. 별도 native 고정 순서 회귀는 첫 wave의 ORDER_NOT_FOUND/CANCELLED 두 결과가 재시도·복구 뒤에도 최초 응답으로 유지되는 것을 확인했다. 자유 실행에서는 후속 주문과 이전 wave 잔량도 영향을 줄 수 있으므로 모든 run에 단일 결과를 강요하지 않았다.

집중 중복에서는 같은 키의 다른 복사본이 완전한 응답을 받더라도 유실한 복사본은 UNKNOWN으로 남았다. 내용 충돌에서는 기존 준비 주문/취소 키에 다른 수량/취소 대상을 넣은409/NOT_ADMITTED를 별도 관측으로 기록했다. canonical 첫 입력/최초 결과·WAL 순번이 변하거나 같은 명령이 중복 저장되면 실패하도록 검사했다. 404 UNKNOWN은 계속 미확정이다.

## 기록 replay와 분석표

첫 반복의 세 profile을 각각 아래 새 폴더에 recorded replay했다.

| profile | 원본 내 선택 명세 | 재현 자료 |
| --- | --- | --- |
| cancel-race | case-000-cancel-race/replay-005.json | var/concurrent-campaign-replay-cancel-20261117 |
| duplicate-storm | case-001-duplicate-storm/replay-005.json | var/concurrent-campaign-replay-duplicate-20261117 |
| id-conflict | case-002-id-conflict/replay-007.json | var/concurrent-campaign-replay-conflict-20261117 |

각 replay54파일의 봉인을 확인했다. runId/tradeId run prefix를 정규화한 전체 최종 checkpoint·각 wave 실제 WAL 순서·동시 snapshot 전체 상태가 원본과 일치했다. 최종 recorded 명세는 bytes도 같았다. PID/포트·시간·OS 스케줄까지 같다는 뜻은 아니다.

새 읽기 전용 보고서가12입력을 분석했고 JSON·Markdown은 새 폴더에서 두 번 생성해 bytes 동일했다. v3 profile/클라이언트 수·의도한409·원래 UNKNOWN·후속 조회/retry·실제 저장 순서를 구분했다. 기존 v2/SIGKILL8입력과 v1/과거10분4입력을 각각 var/concurrent-campaign-v2-recheck-20261117, var/concurrent-campaign-v1-recheck-20261117에 분석했다. 이전 보관 보고서와 name/counts/originalMeasurement/attempts/unknownCommands가 같았고 과거 무효 측정/미확정 관측을 유지했다.

## 회귀·CI와 남은 범위

새 portable 회귀는 결정적 profile/seed/역할 배치와 버전/충돌 명세, 실행 수/시간·용량/flag 상한, 예산 예약/초과 취소·출력/시작 전 실패의 입력과 한 명령 재현 보존을 확인했다. 예산 시험의 실행기만 합성이며 실제 거래 증거가 아니다. native 회귀는 세 실제 profile·관측 replay와 첫 wave의 두 취소 순서를 검사했다. native 보고서 smoke는 실제 캠페인 fixture에서12클라이언트/의도한16충돌과 UNKNOWN 분리를 확인했다.

최초 portable 전체 CI `var/ci/portable.ZUAx0u`는 공용 테스트에서 native 파일에만 있던 failedWriter를 참조해 vet 단계에서 실패했다. campaignFailedWriter를 공용 테스트 파일에 정의하고 관련 회귀·두 최종 전체 CI를 재검증했다. 초기 실패와 먼저 통과한 native `var/ci/native.VC9mjr`는 보존했다.

- 최종 portable: `var/ci/portable.AGxWmP`, Go259개·Python118개·format/vet/build·smoke exit0.
- 최종 native: `var/ci/native.IHoMnt`, Go race306개(명시적 child helper skip1)·Python118개·모든 기존/새 smoke exit0.

선행 보고서 변경은 a97d1e5로 커밋하고 masterf17d5c7으로 병합/푸시했다. [해당 SHA의 GitHub CI44023735396](https://github.com/gaon000/exchange/actions/runs/44023735396)는 portable/native와 모든 step이 success였다. 새 캠페인 변경은 별도 codex/concurrent-scenario-campaign 브랜치의 로컬 구현/검증이며 아직 새 원격 CI로 주장하지 않는다.

자료 예산은 해당 폴더의 논리 bytes이고 파일 시스템 quota가 아니다. 감시/종료 정리 중 초과한 실패 원본을 삭제하지 않으며 시간/용량 중단을 성공으로 바꾸지 않는다. 서버와 같은 엔진을 비교 모델로 사용한다. 무제한 스케줄 탐색·독립 매칭 oracle·장기 운전/현재 최대 처리량·물리 전원 장애 보장을 추가하지 않는다. v1 전체 계획은 필수 전원 장애 증거가 없어 Blocked 유지다.

# 동시 요청·강제 종료 자료 분석 보고서

기존 `transport-report`에 [동시 요청 v2](concurrent-recovery.md)와 [동시 SIGKILL](concurrent-crash-recovery.md) 입력을 추가했다. 원래 클라이언트 UNKNOWN·후속 GET·같은 ID 재시도를 구분하고, client/phase별 실패 단계·유형/지연과 실제 WAL 순서·snapshot/tail 경계를 JSON·Markdown으로 생성한다. 입력을 읽기만 하며 서버 접속·주문/재시도·프로세스 종료·복구/매칭 재실행을 하지 않는다.

## 실행

```sh
python3 tools/diagnostics/experiment.py transport-report \
  --spec docs/benchmarks/20261117-concurrent-report-spec.json \
  --output var/concurrent-report-new
```

[기존 보고서 안내](transport-unknown-report.md)의 새 출력/경로 보호·파일 봉인·실패 정책을 그대로 따른다. 입력 명세 v1의 inputs에 다음 종류를 추가한다. 경로는 명세 파일 기준이다. 추가 dependency는 없다.

```json
{
  "version": 1,
  "inputs": [
    {"name": "동시 요청", "kind": "concurrent", "bundle": "../../var/concurrent-example"},
    {"name": "WAL SIGKILL", "kind": "concurrent-crash", "bundle": "../../var/crash-example", "case": "wal-after-sync"}
  ]
}
```

- `concurrent`: 성공 봉인된 recoverycheck v2 묶음 전체. case/followups는 지정하지 않는다. wave마다 네 initial/네 retry 관측과 계획·live order/실제 WAL·snapshot prefix·재기동 metadata·최초 응답·최종 상태를 대조한다.
- `concurrent-crash`: 성공 봉인된 SIGKILL 묶음에서 독립 run인 case 하나를 선택한다. case는 `wal-after-sync`, `snapshot-before-publish`, `snapshot-after-publish` 중 하나이며 followups는 지정하지 않는다. 서로 다른 case는 같은 bundle에서도 별도 입력으로 지정할 수 있다. 동일 선택 중복은 거절한다.
- 기존 `loadgen`/v1 `recoverycheck` 입력과 출력 필드는 유지한다. 여러 종류를 같은 명세에 넣어도 입력별 결과/분모를 합쳐 성능 판정하지 않는다.

## 출력과 해석

`report.json`, `report.md`, manifest/completed 또는 쓰기 실패의 failed.json을 생성한다. 생성 전에 봉인/자료 일관성 검사가 실패하면 새 출력 폴더를 만들지 않는다. 원래 자료·summary·UNKNOWN·measurementValid를 변경하지 않는다.

| 필드/표 | 내용 |
| --- | --- |
| `attempts`, `transportGroups` | 실패한 원래 시도의 phase/client/요청 ID·단계/유형·지연(ns와 ms), 시도 수와 논리 키 수를 구분 |
| `attempts[].measurementCertainty` | 원래 시도는 UNKNOWN으로 유지. unknownCommands는 키별 관측을 모음 |
| `withinRunResolution` | 원래 INITIAL phase에서 완전한 client 응답을 관측했는지. 중계의 upstream 성공/복구 후 retry를 원래 성공으로 세지 않음 |
| `firstFollowupResolution` | 재시도 전 첫 GET이 기록돼 있으면 해당 관측. 404는 UNRESOLVED, 해당 조회가 없으면 null |
| `subsequentResolution`, `queries` | 이후 GET들의 개별 관측/출처/step·line·stage. 뒤에200을 관측해도 앞404는 지우지 않음 |
| `retryResolution`, `retryObservations` | 같은 ID retry에서 완전한 최초 결과를 관측했는지, 별도 client/phase·순번·응답 hash |
| `execution.groups` | 경계/step·클라이언트·phase 시도·원래 UNKNOWN·snapshot/tail, 실제 고유 WAL 입력 순서 |
| `walOrder[].segment` | SNAPSHOT_PREFIX / RECOVERY_TAIL / AFTER_RETRY. 실제 저장 순번이며 클라이언트 완료 순서가 아님 |
| `upstreamResults` | v2 중계가 받은 결과 순번과 원래 client certainty를 분리. upstream200이 원래 client 성공을 의미하지 않음 |

SIGKILL 과거 관측에는 error 원문과 elapsedNs만 있고 failureStage/errorType은 없다. 단계/유형은 **null·미계측**으로 남기고 문자열에서 EOF·timeout 등을 추정하지 않는다. 원문 오류/본문은 새 보고서에 복제하지 않는다. SIGKILL의 client 오류는 완전한 응답이 없는 일반 TRANSPORT_FAILURE/UNKNOWN으로 집계한다. 재시도 지연을 원래 실패 지연에 합치지 않는다. UTC가 없으면 null이며 mtime으로 추정하지 않는다.

예를 들어 WAL 동기화 직후 죽은 원래 요청은 UNKNOWN, 재시도 전 GET은404/UNRESOLVED, 이후 같은 ID retry는 RESULT_OBSERVED일 수 있다. 세 값은 함께 보존한다. 후속 결과 관측은 원래 어느 복사본이 접수됐는지나 원래 전송 시도의 성공을 증명하지 않는다. **404 UNKNOWN은 계속 미확정**이다.

## 검증 범위

먼저 전체 묶음의 manifest/completed·모든 파일 hash/크기를 확인한다. 선택한 계획·관측의 client/phase/키·본문을 대조하고, WAL의 연속 순번·고유 키·허용 입력·이전 prefix, checkpoint 결과/순번·공급량·기대/실제 상태 일치와 전체 최초 응답을 검사한다. 실제 저장 순서와 기록 replay 순서, snapshot 경계·tail 수, 같은 ID retry 뒤 이전 WAL/최초 결과 불변도 대조한다. 단순 재해시로 모순을 숨기면 거절한다.

SIGKILL은 자식 ready/네 요청 도착/경계 기록과 종료 전 진행 중4개·SIGKILL WaitStatus 확인 자료, 종료 전 받은 성공 응답, crash WAL과 후속 first GET/retry/두 번째 재기동 결과를 대조한다. 원래 실행기가 했던 실제 신호·파일 sync·매칭/복구 검증의 **보관 증거 일관성**을 확인하는 것이다. 이 분석 명령이 그 실행을 다시 수행하거나 독립 매칭 oracle/전원 장애 증거를 추가하는 것은 아니다. 실패·미봉인/중간 실행은 성공 묶음 입력으로 분석하지 않는다.

```sh
python3 -m unittest discover -s tools/diagnostics -p 'test_*report.py'
bash scripts/ci.sh portable
bash scripts/ci.sh native
```

portable 회귀는 합성된 phase/순서 그래프의 UNKNOWN/조회/retry 분리·원본 불변/재생성, 재해시한 순번/중복/입력·공급량·경계/신호·관측 변조, 기록 replay 순서·경로/기존 출력 보호와 출력 실패를 검사한다. 합성 그래프를 실제 거래/신호 증거로 주장하지 않는다. native CI smoke는 CI 소유의 실제 동시 요청 및 세 SIGKILL fixture로 CLI 보고서를 생성/검증하고 재해시한 복구 경계 모순을 거절한다.

[보관 자료의 실제 결과](benchmarks/concurrent-evidence-report.md)와 [재생성 명세](benchmarks/20261117-concurrent-report-spec.json)를 함께 읽는다. 실제 OS·전원 장애는 계속 미완료·보류다.

# 전송 오류·UNKNOWN 분석 보고서

전송 실패의 단계·유형별 건수와 요청 ID·지연, 이후 결과 조회 관측을 보관 자료에서 재생성한다. 읽기 전용 분석이며 서버에 접속하거나 주문·취소·재시도를 제출하지 않는다. 원래 표본·summary·measurementValid를 바꾸지 않고 현재 소스로 만든 Go checker만 실행한다. 실제 전원 장애 검증은 미완료·보류다.

## 실행

저장소 루트에서 Python 3.9 이상과 [Go wrapper](development.md)를 사용한다. 추가 dependency는 없다.

```sh
python3 tools/diagnostics/experiment.py transport-report \
  --spec docs/benchmarks/20261117-transport-unknown-spec.json \
  --output var/transport-report-new
```

`--output`은 입력·후속 조회 묶음·명세 밖의 새 디렉터리여야 하며 parent가 이미 있어야 한다. 비어 있는 기존 폴더와 symlink/그 하위는 거절한다. 보고서 생성 전 명세·자료 검증 실패에는 출력 폴더를 만들지 않는다. 쓰기 실패에는 completed를 게시하지 않고 가능한 경우 failed.json을 남긴다. 입력 원본·보고서·실패 출력을 덮어쓰지 않는다.

| 출력 | 내용 |
| --- | --- |
| `report.json` | 명세/소스 hash, 입력 봉인 검증, 원래 측정 판정, phase·분류·단계·유형별 건수/지연 분포, 요청별 UNKNOWN과 후속 조회 |
| `report.md` | 입력별 집계표·요청 ID/지연·시점별 상태와 각 조회 관측 |
| `manifest.json`, `completed.json` | 출력 파일 크기/hash와 모든 기록이 끝난 뒤 완료 표식 |
| `failed.json` | 출력 중 실패. 성공 완료와 구분 |

부하 자료는 현재 Go checker의 `--verify-plan` 옵션으로 preparation/warmup/measurement의 모든 계획 행을 설정에서 재생성한 계획과 순서·개수·ID/계정·phase·본문/시각까지 엄격히 대조하고, 표본·기존 summary를 재검증한다. 단순 재해시로 계획의 요청 정보를 바꾸면 거절한다. 기존 checker의 두 인자 호출과 출력 형식은 유지한다. 생성기 미전송 때문에 loadgen에 failed.json만 있는 실행도 **상위 묶음이 봉인한 파일**과 checker의 raw hash가 모두 일치하면 분석한다. `loadgenComplete=false`와 기존 measurementValid=false를 그대로 기록하며 실패를 숨기거나 유효 측정으로 바꾸지 않는다.

## 입력 명세 v1

`inputs`는 1~32개이며 이름과 선택 run의 중복을 거절한다. 경로는 명세 파일의 디렉터리 기준이다.

```json
{
  "version": 1,
  "inputs": [
    {"name": "부하 실행", "kind": "loadgen", "bundle": "../../var/example-suite", "case": "on-1"},
    {"name": "응답 유실", "kind": "recoverycheck", "bundle": "../../var/example-fault"}
  ]
}
```

- `loadgen`: manifest/completed 또는 기존 artifact-manifest가 있는 suite의 한 case를 선택한다. `case/loadgen/`의 plan·samples·preparation·summary·run과 선택적 생성기 진단을 현재 checker로 검증하고, 읽는 파일이 상위 manifest에 포함됐는지 확인한다. 보관된 바이너리는 실행하지 않는다.
- `recoverycheck`: `sh scripts/random-recovery.sh --transport-loss`의 봉인된 성공 자료를 선택한다. scenario hash·완료/summary·동일한 최종 checkpoint·최초 결과·fault/event/지연·GET query trace를 대조한다. 실제 거래나 WAL 복구를 분석 명령에서 다시 실행하지 않는다. 원래 복구 검증 결과와 이번 자료 일관성 검증을 구분한다.

부하 실행의 별도 후속 조회 자료는 다음처럼 선택한다. 입력 하나당 최대 64개다.

```json
"followups": [
  {"bundle": "../../var/receipt-bundle", "result": "unknown-followup/result.json", "ready": "unknown-followup/server-query.json"}
]
```

`result`와 `ready` 모두 해당 묶음의 봉인 목록에 있어야 한다. ready의 runId와 result의 account/kind/id/measuredIndex/measurementCertainty를 원래 UNKNOWN 표본에 연결한다. result는 `httpStatus`, `responseBytes`, `response`를 포함하고 JSON body와 decoded 객체가 같아야 한다. HTTP200은 응답의 버전·run·계정·명령 종류·요청 ID·양수 순번·결과 코드를 확인한다. 같은 키의 완전한 응답 bytes가 다르면 거절한다. 이 검사는 후속 결과와 원래 전송 시도의 접수 여부를 동일시하지 않는다.

고정 장애 자료의 후속 조회는 자체 http.jsonl에서 같은 계정/kind/ID의 fault step 이후 GET만 선택한다. 원래 transport 파일·최종 checkpoint의 최초 결과와 일치해야 한다. 조회 시각이 없으면 null/미계측이며 파일 mtime으로 추정하지 않는다. `orderBasis`에 명세 순서 또는 scenario step/trace line을 기록한다. `200` 뒤 `404`가 관측되어도 앞서 확인한 결과 관측을 지우지 않고 두 행을 모두 보존한다.

## 시점과 분모

- `attempts[].measurementCertainty`는 원래 UNKNOWN이다. 완전한 응답이 없는 전송 실패를 성공으로 바꾸지 않는다.
- `withinRunResolution`은 같은 run의 원래 부하 표본에 완전한 BUSINESS 응답이 있는지 나타낸다. 같은 키의 중복 시도가 해결된 경우에도 각 실패 시도는 UNKNOWN으로 남는다.
- `subsequentResolution`은 **별도 후속 GET**에서 해당 키의 결과를 확인했는지 나타낸다. `200`은 RESULT_OBSERVED, `404 UNKNOWN`·`202 PENDING`만 있으면 UNRESOLVED다. `200`도 어느 원래 시도가 접수됐는지, 재시도 전부터 완료였는지를 증명하지 않는다.
- 원래 본 측정 `unknownLogicalCommands`와 measurementValid는 Go 재집계 값 그대로다. 후속 조회를 측정 성공 건수·실패율·처리량·지연 분포에 섞지 않는다.
- 단계·유형별 `attempts`와 `logicalKeys`는 다르다. logical key는 run/계정/명령 종류/ID이며 PREPARE/WARMUP/MEASURE/FAULT_SCENARIO를 나누어 집계한다. 여러 입력이나 장애 자료를 합쳐 성능 표본으로 판단하지 않는다.
- 지연의 JSON 단위는 ns, Markdown은 ms다. loadgen은 finishedNanos-startedNanos다. 별도 monotonic 값이 없는 preparation 시도는 null이다. 새 장애 재현은 executor 호출 직전부터 반환까지의 monotonic 경과다. 지연 분포는 관측된 값만으로 nearest-rank p50/p99/max를 계산하고 관측·누락 수를 함께 표시한다.
- 과거 failureStage/errorType/지연 부재는 null/미계측이다. 관측한 round_trip/other와 구분하고 과거 원인을 소급 추정하지 않는다. 오류 원문과 요청 본문은 새 보고서에 복제하지 않는다.

## 검증과 근거

[실제 결과](benchmarks/transport-unknown-report.md)와 [재생성 명세](benchmarks/20261117-transport-unknown-spec.json)를 따른다. 원본은 Git 제외 var에 있으므로 새 clone만으로 과거 보고서를 재생성할 수 있다는 뜻은 아니다.

```sh
python3 -m unittest discover -s tools/diagnostics -p 'test_transport_report.py'
bash scripts/ci.sh portable
bash scripts/ci.sh native
```

회귀 검사는 과거 진단/지연 부재, 404/202·200 시점 분리, 같은 ID/다른 명령 종류, 재시도와 phase 분모, 봉인/identity/순번/지연 의미 변조, 경로·기존 출력 보호와 Markdown escape를 포함한다. native smoke는 새 부하 자료와 CI 소유의 실제 장애 재현 자료로 CLI 보고서를 생성·검증하고 재사용·재해시한 raw 의미 변조를 거절한다. 소프트웨어 자료 검증을 전원 장애 검증으로 대체하지 않는다.

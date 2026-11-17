# 동시 요청·강제 종료 자료 보고서 실제 검증

2026-11-17, master2f9a760 이후 기능 브랜치에서 읽기 전용 보고서 adapter를 구현했다. 동시 요청 원본/replay 각1개와 SIGKILL 원본/replay의 세 case씩, 총8개 선택 입력을 JSON·Markdown으로 생성했다. 실제 고유 원본 묶음4개·봉인 파일276개를 검증했고 새 폴더의 두 보고서가 **JSON·Markdown 모두 bytes 동일**했다. 실제 OS·전원 장애는 계속 미완료·보류이며 성능 측정·거래/복구 재실행은 하지 않았다.

[실행/필드 안내](../concurrent-evidence-report.md), [입력 명세](20261117-concurrent-report-spec.json), [검증 요약 JSON](20261117-concurrent-evidence-report.json)을 함께 읽는다. 완전한 자동 보고서/봉인은 아래 Git 제외 var에 보존했다. 새 clone에는 과거 원본이 포함되지 않는다.

```sh
python3 tools/diagnostics/experiment.py transport-report --spec docs/benchmarks/20261117-concurrent-report-spec.json --output var/concurrent-report-final-20261117
python3 tools/diagnostics/experiment.py transport-report --spec docs/benchmarks/20261117-concurrent-report-spec.json --output var/concurrent-report-final-reproduced-20261117
```

이미 존재하는 출력이므로 재실행 시 새 이름을 사용한다. JSON hash는 `b63bacaf030c18541c4cdfda123880e260c639d2925f2015cedb0641efca56a4`, Markdown hash는 `d161fbe52397f40f1a77b8207cc2490e5dcc6db92a2a446415ea53c5b7aca369`이다. sourceSHA256은 그 보고서를 만든 소스 기준이며 이후 소스 변경 시 새 보고서 hash가 달라질 수 있다.

| 입력 | 원래 UNKNOWN 시도/논리 키 | 재시도 전 첫 GET 미확정 키 | snapshot + 복구 tail | 단계/유형 미계측 시도 |
| --- | --- | ---: | --- | ---: |
| 동시 요청 원본 | 6 / 4 | 2 | 3+3, 9+3 | 0 |
| 동시 요청 기록 replay | 6 / 4 | 2 | 3+3, 9+3 | 0 |
| SIGKILL WAL 원본 | 4 / 3 | 2 | 2+1 | 4 |
| SIGKILL snapshot 전 원본 | 4 / 3 | 0 | 2+4 | 4 |
| SIGKILL snapshot 후 원본 | 4 / 3 | 0 | 5+1 | 4 |
| SIGKILL WAL replay | 4 / 3 | 2 | 2+1 | 4 |
| SIGKILL snapshot 전 replay | 4 / 3 | 0 | 2+4 | 4 |
| SIGKILL snapshot 후 replay | 4 / 3 | 0 | 5+1 | 4 |

동시 요청의 buy 키는 재시도 전 GET을 기록하지 않아 firstFollowupResolution=null이다. held 키는404/UNRESOLVED를 기록했다. WAL SIGKILL의 buy/cancel 키는 재시도 전404, rest 키는200을 관측했다. 이후 모든 키의 retry/두 번째 재기동 GET에서 최초 결과를 관측해도 원래 UNKNOWN은 그대로 남는다. 원본과 replay는 별도 실행 증거이며 표의 건수를 합쳐 성능 표본으로 해석하지 않는다.

v2의 단계는 response_headers/eof와 response_read/unexpected_eof를 그대로 집계했다. SIGKILL 자료에는 단계/유형 필드가 없으므로24개 시도 모두 null·미계측으로 표시했다. raw Error 문자열이나 요청 본문은 새 보고서에 복제하지 않았다. 보관된 client elapsedNs는 원래 실패 지연으로 표시하며, 재시도 시간/성공을 원래 측정에 합치지 않았다.

## 기존 자료와 회귀

기존 `docs/benchmarks/20261117-transport-unknown-spec.json`의4입력을 `var/concurrent-report-legacy-recheck-20261117`에 재생성했다. 과거 저장 JSON과 name/counts/originalMeasurement/attempts/unknownCommands가 완전히 같았다. 10분 snapshot 끔/켬은 measurementValid=false를 유지했고 켬의 원래 UNKNOWN1건·후속404도 미확정으로 유지했다. 과거 지연 미계측 v1과 계측 v1 자료의 의미도 유지했다.

Python6개 새 회귀는 원래 UNKNOWN/조회/retry 분리·원본 불변/반복 bytes, 실제 입력/순번·공급량·snapshot/tail·신호·관측 변조와 recorded 순서, 경로/기존 출력 보호·출력 실패를 검사했다. 기존 namespace 구분 회귀도 유지했다. WAL/live-order 또는 기대/실제 snapshot 양쪽을 바꾼 뒤 재해시해도 순번·공급량 검증이 거절했다. 합성 phase 그래프를 실제 매칭 증거로 사용하지 않는다.

전체 로컬 CI는 portable `var/ci/portable.1B2ZjU`의 Go255개·Python117개, native `var/ci/native.pqvvwK`의 Go race301개(명시적 child helper skip1)·Python117개와 format/vet/build·모든 smoke가 exit0이었다. native smoke는 CI가 실제 실행한 v2 동시 요청과 SIGKILL 세 case를 CLI로 분석하고 원래 UNKNOWN/첫 GET/retry 분리, 기존 출력 거절, 재해시한 복구 snapshot999 모순 거절을 확인했다.

선행 SIGKILL 구현은 da984c7로 커밋하고 master2f9a760에 병합/푸시했다. [해당 SHA의 GitHub CI44018964216](https://github.com/gaon000/exchange/actions/runs/44018964216)는 portable/native 모두 success였다. 이번 보고서 변경의 검증은 위 별도 로컬 CI이며 아직 새 원격 CI 결과로 주장하지 않는다.

이 보고서는 봉인된 저장 순서·최초 응답·checkpoint/시점 관측의 일관성 분석이다. 원래 신호/동기화·복구/매칭을 다시 실행하거나 독립 매칭 oracle·전원 장애 내구성·현재 최대 처리량을 새로 증명하지 않는다. v1 전체 계획은 전원 장애 증거가 없어 Blocked를 유지한다.

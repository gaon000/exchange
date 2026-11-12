# 실험 자료 용량·참조와 정리 미리보기

상태: Completed
종료일: 2026-11-12

## Purpose / Big Picture

개발자가 var의 항목별 논리/할당 용량·참조 관계와 보존 사유를 확인하고, 삭제 전 검토할 후보 목록을 JSON/Markdown으로 얻는다. 실제 측정·실패 원본·고정 빌드·아카이브·실행 데이터와 전이 의존성을 보호한다. 이번 요청은 삭제 예정 목록 확인까지이며 기존 자료를 삭제/이동/압축하지 않는다. 전원 장애 검증과 새 성능 측정은 계속 보류한다.

## Progress

- [x] 기존 manifest/실행 재개/고정 빌드·문서 인용 형식 확인 및 feature branch 생성.
- [x] 읽기 전용 inventory·참조 그래프·보존 분류와 JSON/Markdown 계획 구현.
- [x] 경계/누락/심볼릭 링크/변경 중인 입력 회귀와 실제 var 미변경 검증.
- [x] 문서·전체 CI·완료 기록.

## Decision Log

- 2026-11-12: 사용자 요청으로 검토·커밋·마스터 병합/푸시·GitHub CI 확인을 진행한다. 검토 중 보관용 이름의 폴더 자체가 실험 root인 경우 내부를 나누던 경계를 발견해, root에 실험/봉인/실행 표시가 있으면 전체 단위로 보호하도록 수정했다. 네 보관용 이름의 회귀를 추가하고 전체 Python 44개 및 실제 자료 보존을 재검증했다.
- 2026-11-12: 기본은 조회/미리보기다. 명시적 임시 정책에 일치하면서 보호/참조 사유가 없는 항목만 후보로 삼는다. 모르는 항목은 검토 필요로 유지한다. 오래되거나 크다는 이유만으로 후보가 되지 않는다.
- 2026-11-12: 실험 묶음은 단위 전체로 다루고 내부 파일을 정리 대상으로 나누지 않는다. 고정 빌드·실패/중단 원본·실행 중 데이터·요청 파일·아카이브는 임시 정책보다 우선 보호한다.
- 2026-11-12: 숫자 용량과 참조 위치만 기록하고 파일 본문은 보고서에 복사하지 않는다. 새 의존성 없이 Python 표준 라이브러리를 사용한다.

## Surprises & Discoveries

과거 문서는 brace 축약 경로와 rawReportSHA256만으로 원본을 인용하기도 한다. 생성한 retention 보고서는 형식/완료 해시를 확인한 뒤 참조 스캔에서 제외하여 모든 항목을 영구 보호하는 순환을 방지했다. 외부에서 이 보고서 자체를 해시로 인용하는 관계는 유지한다. 기존 archive receipt가 유일한 과거 측정 원본을 가리킬 수도 있다. 초기 실제 조사에서 nested storage/loadgen manifest를 diagnostic 봉인 형식으로 해석해 스캔 제한을 기록했으므로, diagnostic manifest 분류는 단위 root에만 적용하도록 수정했다. 제한된 조사는 후보를 모두 제외했고 기존 데이터는 변경하지 않았다. 구체 경로가 없는 metadata에는 정규식을 적용하지 않도록 검색 비용도 줄였다.

## Outcomes & Retrospective

`artifacts.py`와 명시적 보존/임시 정책, 사용 안내와 12개 회귀 검사를 구현했다. 구현 시점의 실제 조사에서 163개 보호·62개 검토 필요·중간 미리보기 2개만 후보로 분류했다. 2,182개 참조 관계를 기록했다. 기존 파일 4,792개와 주요 제어 파일 428개의 해시가 유지됐으며 실제 삭제/이동/압축은 수행하지 않았다. 고정 빌드·재개 부모 연쇄·원본 archive·최신 데모·이전 CI 실패 및 경로 없는 SHA 인용 원본의 보호도 확인했다. 발행 전 검토의 수정/재검증은 아래에 별도로 기록한다. 커밋·병합·push 상태는 Git 기록, 원격 CI는 해당 마스터 커밋의 Actions 실행 결과를 확인한다. 전원 장애는 계속 보류다.

## Validation and Acceptance

작은 Git fixture에서 경로/brace/hash 인용·전이/순환 참조·고정 빌드·실패·최신 CI·archive·symlink·입력 변경·읽기 실패·정책/출력 재사용을 검증한다. 실제 var의 파일 목록/size/mtime와 주요 제어 파일 해시를 실행 전후 비교한다. 전체 Python 검사와 portable/native CI를 실행한다. 실제 삭제 기능은 이번 구현 범위 밖이다. 원격 CI는 push 뒤 해당 마스터 커밋으로 확인한다.

실제 검증:

- 초기 회귀의 macOS 임시 경로 alias/fixture 경계를 수정한 뒤 개별 검사와 전체 Python 검사를 통과했다. 초기 실패 로그는 `var/artifact-retention-tests.log`에 보존했다.
- `bash scripts/ci.sh portable`: Go 215개·21 package, Python 43개와 진단 smoke·포맷/vet/build 통과(`var/artifact-retention-ci-portable-accepted.log`, `var/ci/portable.H8Yqdz`).
- `bash scripts/ci.sh native`: race 249개·21 package, 명시된 child-helper skip 1개, Python 43개·APFS/HTTP 복구 smoke 통과(`var/artifact-retention-ci-native-accepted.log`, `var/ci/native.gaX55V`).
- 마지막 출력 경로 `..` 우회 차단 보강 후 `python3 -m unittest discover -s tools/diagnostics -p 'test_*.py'` 43개를 재통과했다(`var/artifact-retention-python-accepted.log`). Go source/의존성은 변경하지 않았다.
- 최종 실제 출력: `var/artifact-reports/20261112-accepted/retention-report.json`, `plan.json`, `report.md`, `completed.json`. `scanComplete=true`, 약 30.67GB 논리 용량, 후보 할당 합계 상한 2,039,808 bytes다. 파일 단위 할당은 APFS의 실제 회수 공간을 보장하지 않는다.
- 보존 검증: `var/artifact-retention-preservation.json` PASSED. 최종 스캔/전후 검증 3.17초, 기존 파일 크기/mtime·제어 파일 해시 변경 0, 출력 외 예상하지 못한 파일 추가 0이다.
- 발행 전 검토: 보관용 폴더 자체가 실험 root일 때 indivisible 보호를 적용한 뒤 전체 Python 44개 통과(`var/artifact-retention-review-python.log`). 실제 재조사 `var/artifact-reports/20261112-reviewed`는 `scanComplete=true`, 보호 168개·검토 60개·중간 보고서 후보 2개·참조 2,191개다. `var/artifact-retention-review-preservation.json`은 기존 파일 4,798개와 제어 해시 429개 유지, 예상하지 못한 추가 0으로 PASSED다.
- Markdown 경로·새 파일/추적 변경 공백 검사를 수행했다. 성능 부하 측정이나 실제 데이터 정리는 실행하지 않았다.

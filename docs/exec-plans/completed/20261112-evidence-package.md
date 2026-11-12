# 실험 자료 패키징·복원과 재검증

상태: Completed
종료일: 2026-11-12

## Purpose / Big Picture

선택한 봉인 실험·비교 명세에 필요한 이전 실행과 고정 producer 빌드를 함께 패키징하고, 원본을 보존하며 새 폴더에 복원한다. 복원 자료에서 무결성/재개 연쇄/고정 빌드를 검증하고 분석 및 JSON/Markdown 비교 보고서를 재생성한다. 파일 bytes와 원래 manifest는 수정하지 않는다. 동일 디스크의 복원 시험과 별도 장치 백업·실제 전원 장애는 구분한다.

## Progress

- [x] 기존 봉인/재개/고정 빌드·분석/비교 경로 계약 확인, feature branch 생성.
- [x] 의존성 수집·불변 패키지·제한된 안전 복원·복원 영역 전용 경로 해석 구현.
- [x] 복원 자료 검증·분석/비교 재생성과 실패·손상·경로/원본 차단 검증.
- [x] 실제 보관 자료 복원·원본 보존·문서·전체 CI 검증.

## Decision Log

- 2026-11-12: 후속 요청으로 변경 검토·커밋·master 병합/푸시와 해당 마스터 커밋의 실제 GitHub CI 확인을 진행한다. 검토에서 개발 명령 표의 pipe 구분자가 열을 나누는 문서 오류를 수정했다.

- 2026-11-12: Python 표준 라이브러리로 새 `evidence.py` CLI를 제공한다. 현재 봉인 collection/legacy inventory와 고정 build, 비교 spec·criteria를 대상으로 한다. raw historical whole-archive만 남은 입력은 자동 재구성하지 않고 명확히 거절한다.
- 2026-11-12: protocol.resumeFrom와 producer-build의 고정 출력 경로, 비교 입력·criteria/config/workload를 따라 필요한 단위를 수집한다. 모든 파일의 해시를 기록하고 원본의 변경/추가·symlink/특수 파일을 거절한다.
- 2026-11-12: 복원은 package inventory에 정확히 포함된 정규 파일/디렉터리만 새 폴더에 생성한다. 압축 해제 경로·중복·링크·용량을 검사하며 기존 출력/원본 내부 쓰기는 거절한다. 실패 중간 출력은 보존한다.
- 2026-11-12: 경로 해석은 원래 checkout과 복원 payload의 대응만 허용하며 패키지 밖 fallback은 없다. 기존 metadata의 bytes/hash는 유지한다. 생산 바이너리는 해시/Go build 정보만 확인하고 실행하지 않는다.

## Surprises & Discoveries

실제 `var/lower-rate-reference`와 `var/repro-tools-timer-smoke-allowed`에는 봉인 뒤 추가된 `.DS_Store`가 있었다. 기존 무결성 검사가 이를 거절했으므로 원본을 수정하거나 자동 무시하지 않았다. 실패 명세와 거절을 보존한 뒤, P6의 완료 case 두 개를 원래 완료 파일 그대로 새 봉인 wrapper로 복사하고 타이머 collection은 unrecorded root `.DS_Store`만 제외한 명시적 사본으로 검증했다. 원래 manifest와 사본의 대응 및 출처를 기록했다. 실제 dirty 부모 연쇄의 패키징 성공을 주장하지 않으며, 원본이 없는 3단계 재개 연쇄와 완료 prefix의 보존은 합성 fixture에서 확인했다.

resumeFrom는 원래 절대 경로와 부모 manifest hash를 보존한다. 비교 spec은 상대 경로를 사용하고 기준 JSON은 config/workload를 상대 참조한다. 복원에 맞춰 파일을 다시 쓰면 원래 봉인이 깨지므로, 검증/비교에 별도의 제한된 경로 해석을 전달해야 한다.

## Outcomes & Retrospective

표준 라이브러리 CLI와 9개 회귀 검사, CI의 원본 비가용 복원 smoke를 구현했다. 선택한 collection·resume 부모 연쇄·고정 producer 빌드·비교 명세/criteria/config/workload를 묶고, 복원 영역 전용 경로 해석으로 검증·분석 및 JSON/Markdown 비교표를 재생성한다. 원본 metadata bytes는 수정하지 않으며 보관 생산 바이너리는 실행하지 않는다. package/restore index를 자료 정리의 보호 표시에도 추가했다.

실제 보관 P6 정상·snapshot 완료 실행과 타이머 사본, 고정 빌드 등 7개 단위·115개 entry, 논리 471,355,157 bytes를 패키징했다. 압축 payload는 89,431,180 bytes다. 복원 자료에서 P6 2개·타이머 4개 case의 분석과 비교 2개 group을 생성하고, 복원 전후 수치·조건·build 차이·채택 제한이 일치했다. 원본 3개 root의 파일 집합과 metadata 161개 entry도 유지됐다. 비교 자체는 독립 반복 부족/같은 실행 중복/조건 차이 때문에 성능 채택 근거가 아니다.

새 성능 캠페인·전원 제어·기존 자료 삭제/이동·외부 업로드는 하지 않았다. 같은 디스크에서의 검증이며 실제 전원 장애는 미완료·보류다. 이번 구현 요청에서는 커밋·병합·push와 GitHub 원격 CI를 수행하지 않았다.

## Validation and Acceptance

작은 Git/봉인 fixture에서 재개 연쇄·빌드/기준 의존 수집·원본 없는 복원·같은 해시·보고서 재생성을 확인한다. archive traversal/중복/링크/손상/부분/초과 입력·원본/출력 재사용·원본 경로 fallback을 거절한다. 실제 보관된 작은 timer 비교 및 재개 자료에서 새 복원과 검증/분석/비교를 실행하고 원본 size/mtime/hash를 대조한다. 전체 Python·portable/native CI와 문서 경로/공백을 확인한다. 실제 전원 장애 검증은 계속 보류다.


실제 검증 결과:

- `python3 -m unittest discover -s tools/diagnostics -p 'test_*.py'`: 53개 통과. 그중 새 패키징 9개는 원본 제거 후 재생성·고정 빌드 header만 읽기·3단계 부모/완료 prefix·P6 완료 prefix·누락/해시/의미 손상·tar 경로/중복/링크/비정규 type·PAX 압축 해제 상한·출력 재사용을 확인했다.
- 최종 `bash scripts/ci.sh portable`: 215 tests / 21 packages, Python 53개, 원본을 숨긴 timer package/restore/reproduce smoke, 포맷·vet·build 통과. `var/evidence-package-ci-portable-final.log`, `var/ci/portable.0KCygA/summary.txt`에 보존했다.
- 최종 `bash scripts/ci.sh native`: race 249 tests / 21 packages, 명시된 child-helper skip 1개, Python 53개와 archive/prune한 native HTTP/재시작·trace·I/O fixture의 복원/재생성 smoke 통과. `var/evidence-package-ci-native-final.log`, `var/ci/native.pzpN09/summary.txt`에 보존했다.
- 최초 sandbox 실행은 loopback bind의 `operation not permitted`로 실패했다(`var/ci/portable.BKmKr8`, `var/ci/native.WKcoDW`). 해당 기록을 보존하고 loopback/프로세스 관측 권한이 있는 실행으로 재검증했다. 중간 통과도 `var/ci/portable.Yw9XmY`, `var/ci/native.1d7lpa`에 보존했다.
- 실제 자료 거절: `var/evidence-package-validation-20261112/spec.json`, `strict-rejection.json`. 원본을 변경하지 않았고 실패한 입력을 성공 자료로 바꾸지 않았다.
- 명시적 사본/출처와 비교 명세: 같은 폴더의 `retained-p6`, `retained-timer`, `spec-clean.json`, `before-comparison`. 원본 보존·115 entry 복원·비교 일치는 `roundtrip-proof.json` PASSED다.
- 패키지 `var/evidence-package-20261112`, 새 복원 `var/evidence-restore-20261112`, 최종 재생성 `var/evidence-reports-20261112-final`: verification·P6/timer 분석·비교 JSON/Markdown·완료 생성. `final-report-check.json`은 최종 코드로 비교 수치·판정 일치를 다시 확인했다. archive/index와 보고서는 원본을 복사한 로컬 Git 제외 자료이며 별도 장치 백업은 아니다.
- Markdown 상대 경로/공백·추적/새 파일 공백을 확인했다. 사용 명령·지원 입력·크기 제한과 실패 계약은 [패키징·복원 안내](../../evidence-packages.md)로 연결한다.

발행 전 검토에서는 원본/출력 경계·resume 연쇄·고정 빌드 수집·압축 member 검사·복원 전용 resolver·현재 검사기 실행과 실패 기록을 확인했다. 현재 source/config 213개 해시가 최종 실제 재생성 보고서의 해시와 모두 같음을 확인했고, Python 53개를 다시 통과했다. 소프트웨어 동작 수정은 없으며 명령 표의 Markdown 구분자만 수정했다. master 병합/푸시 결과는 Git 기록, 원격 검증 결과는 해당 커밋의 Actions 실행을 확인한다.

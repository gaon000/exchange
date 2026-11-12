# 실험 자료 패키징·복원과 재검증

`tools/diagnostics/evidence.py`는 선택한 봉인 실험과 비교 명세의 의존성을 수집해 새 패키지에 복사한다. 원본을 삭제·이동하거나 manifest의 절대 경로를 수정하지 않는다. 새 폴더에 복원한 자료만으로 무결성·재개 연쇄·고정 빌드 검증, 원본 재집계와 JSON/Markdown 비교 보고서를 재생성한다. 거래 프로세스나 새 부하 측정을 실행하는 명령은 아니다.

## 실행

저장소 루트에서 Python 3.9 이상과 [개발 환경](development.md)의 정확한 Go를 사용한다. 추가 Python 패키지는 없다. 출력의 parent는 미리 존재해야 하며, 출력 세 곳은 각각 **새 이름의 형제 폴더**로 지정한다.

```sh
python3 tools/diagnostics/evidence.py create --comparison config/comparisons/lower-rate-integer-v1.json --output var/evidence-pack-001
python3 tools/diagnostics/evidence.py restore --package var/evidence-pack-001 --output var/evidence-restore-001
python3 tools/diagnostics/evidence.py reproduce --restored var/evidence-restore-001 --output var/evidence-reports-001
```

위 비교 명세의 보관 입력이 모두 존재하고 봉인과 현재 파일 집합이 일치해야 첫 명령이 성공한다. `--bundle var/lower-rate-resumed-2`처럼 collection을 직접 선택하거나 `--bundle`, `--comparison`을 여러 번 지정할 수 있다. bundle만 선택하면 검증·분석을 생성하며 비교표는 선택한 비교 명세가 있을 때 생성한다. 다른 source checkout은 `create --repo /실제/checkout`으로 지정한다. 모든 의존성은 그 checkout 안에 있어야 한다.

선택한 비교 명세가 P6 개별 실행을 가리키면 해당 실행을 포함한 바깥 봉인 collection을 함께 담는다. collection의 `protocol.resumeFrom.path`와 부모 manifest SHA-256을 따라 이전 실행을 재귀적으로 포함한다. `producer-build.json`의 고정 build 출력과 동일한 `build.json`, 다섯 생산 바이너리도 수집한다. 진단 suite의 자체 `bin`도 포함된다. 비교 명세의 criteria와 criteria의 config/workload 파일을 함께 담는다. 자유 텍스트나 임의 shell 명령에서 의존성을 추측하지 않는다.

## 산출물과 검증 범위

| 위치 | 내용 |
| --- | --- |
| package / `payload.tar.gz` | repo 상대 경로의 정규 파일과 디렉터리. 선택한 collection은 통째로 포함하며 중복 선택 경로는 한 번만 저장 |
| package / `package-index.json` | origin checkout, 전체 파일 크기·mode·SHA-256, 선택 단위·의존 그래프·비교 명세, 입력 검증과 도구 해시 |
| package / `completed.json` | index와 payload SHA-256. 두 파일이 모두 일치해야 복원 가능 |
| restore / `data/` | 원래 repo 상대 경로 구조와 파일 bytes·권한을 복원한 자료 |
| restore / `restore-index.json` | 원래 package index와 해시. 원래 절대 경로를 복원 영역으로 해석하는 기준 |
| reports / `verification.json` | 복원 파일 전체 해시, 원래 봉인, 하위 archive, resume 연쇄, 고정 빌드 재검증 |
| reports / `analysis-*.json` | 완료된 진단 suite의 기존 분석, P6 완료 실행의 현재 Go 원본 재집계. 다음 quiet gate에서 중단된 collection의 완료 prefix도 재집계 |
| reports / `comparison-*/report.json`, `report.md` | 기존 [비교 계약](experiment-comparison.md)에 따른 수치·조건·유효성·채택 제한. 각 비교 폴더도 봉인 |
| reports / `completed.json` | 모든 요청 검증·재집계·비교 입력의 의미 검사 성공. 성능 목표 달성을 의미하지 않음 |

복원기의 경로 해석은 원래 checkout 경로와 복원 영역의 **index에 등록된 경로**만 허용한다. 원본이 현재 존재하더라도 원본으로 fallback하지 않는다. manifest와 resumeFrom의 bytes는 그대로 두고 검사에 제한된 resolver를 전달한다. 현재 checkout의 Go 검사기를 새로 빌드해 실행하고, 보관 생산 바이너리는 해시와 `go version -m`의 build header만 읽는다. 보고서에는 검사 코드 해시를 남긴다. 패키지 자체에 현재 분석 도구의 전체 source/toolchain을 vendoring하지 않으므로 보고서 재생성에는 이 checkout과 Go가 필요하다.

P6 원본 재집계는 표본·기존 완료/복구 자료의 검증이다. 거래 엔진을 재기동하거나 보관 WAL에 새로운 요청을 쓰지 않는다. 진단/무효/조건 차이/중복 실행은 기존 비교표에 그대로 남는다. 검증 가능한 자료와 성능 채택 가능한 자료를 구분하며, 비교의 어느 입력이라도 evidence error이면 최종 reproduction 완료를 거절한다.

## 제한과 실패 보존

- 실행이 멈춘 자료를 선택한다. 원본의 파일 집합·inode/size/mtime/ctime·mode를 수집 전후 대조하고 변경되면 실패한다. 모든 원본 파일은 해시로 확인한다.
- 패키지당 정규 파일 논리 합계는 4 GiB, entry 100,000개, 의존 단위 100개까지다. 압축/복원 예상 쓰기량과 별도로 여유 8 GiB를 요구한다. JSON index는 64 MiB 이하다. 경계 초과 시 자동 분할하거나 원본을 정리하지 않는다.
- 경로 이탈·중복 member·symlink/hardlink·특수 파일·해시/크기 불일치·누락·과도한 압축 해제를 거절한다. `extractall`은 사용하지 않는다. 파일 mode는 일반 rwx 권한만 보존하며 ownership·특수 권한·timestamp 복원은 계약에 포함하지 않는다.
- 기존 출력은 비어 있어도 재사용하지 않는다. 원본/봉인 묶음 내부나 symlink parent에 출력하지 않는다. reports도 restore 밖에 둔다. macOS `/tmp` 같은 별칭 대신 `/private/tmp` 등 실제 경로를 사용한다.
- `.DS_Store` 등 봉인 이후 추가 파일도 기존 무결성 계약대로 거절한다. 자동 무시·삭제·재봉인하지 않는다. 원본을 유지해야 한다면 별도의 명시적 사본을 검토하고 원래 파일과의 대응을 기록한다.
- 현재 diagnostic `manifest.json`/legacy `artifact-manifest.json` collection을 지원한다. 하위 case archive는 검증·복사한다. 역사적 collection 전체가 tar/receipt만 남은 입력은 자동으로 풀어 재구성하지 않으며 명확히 거절한다.
- 쓰기 시작 후 실패한 출력에는 `failed.json`을 남기고 다음 실행은 새 이름을 사용한다. 원본은 롤백·삭제하지 않는다. package/restore index가 있는 자료는 [보관 미리보기](artifact-retention.md)에서도 보호한다.

SHA-256과 완료 기록은 손상과 불일치를 찾는 무결성 검사이며 외부 서명이나 신뢰할 수 없는 발행자의 진위 증명은 아니다. 동일 디스크의 패키징·복원 성공은 별도 장치 백업이나 실제 OS·전원 장애 검증을 대신하지 않는다. **실제 전원 장애 검증은 미완료·보류다.**

## 검증

```sh
python3 -m unittest discover -s tools/diagnostics -p 'test_*.py'
bash scripts/ci.sh portable
bash scripts/ci.sh native
```

CI의 짧은 timer/native 거래 fixture는 패키징·복원 뒤 자신이 생성한 원본 bundle/spec을 잠시 숨기고 복원 자료만으로 분석·비교를 재생성한다. 회귀 검사는 이전 실행 연쇄/고정 빌드, 원본 제거 후 경로 해석, P6 완료 prefix, 손상·부분·의미 모순·경로 이탈·링크·압축 해제 상한과 출력 재사용을 검사한다. 실제 보관 자료의 사본 비교와 원본 보존, 엄격한 거절 결과는 [완료 계획](exec-plans/completed/20261112-evidence-package.md)에 기록했다.

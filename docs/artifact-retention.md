# 실험 자료 보관·정리 미리보기

`tools/diagnostics/artifacts.py`는 Git 제외 `var`의 용량·참조 관계·보존 사유를 조사하고 삭제 전 검토할 후보 목록을 만든다. **읽기 전용 미리보기 도구**이며 삭제·이동·압축을 수행하는 옵션은 없다. 원본 정리는 생성한 계획을 검토한 별도 작업으로 진행한다. 새 성능 측정이나 전원 장애 검증을 시작하지 않는다.

## 실행

저장소 루트에서 Python 3.9 이상과 Git을 사용한다. 추가 패키지는 없다.

```sh
# 파일을 만들지 않고 전체 JSON을 stdout으로 확인
python3 tools/diagnostics/artifacts.py

# 새 경로에 JSON과 Markdown 보고서 보관
python3 tools/diagnostics/artifacts.py --output var/artifact-reports/review-001
```

기존 출력 폴더는 비어 있어도 거절한다. 저장소 안의 출력은 `var/artifact-reports` 바로 아래의 새 폴더만 허용하여 기존 봉인 묶음이나 source에 보고서를 섞지 않는다. 다른 위치를 쓰면 저장소 밖의 기존 parent 아래 새 경로를 지정한다. 출력 경로의 `..`와 symlink parent는 거절한다. macOS의 `/tmp` 같은 symlink 별칭을 사용할 때는 `/private/tmp` 등 실제 경로를 사용한다. `--repo`로 다른 checkout을 지정할 수도 있다.

| 산출물 | 내용 |
| --- | --- |
| `retention-report.json` | 스캔 시각·도구 SHA-256·정책·완료/제한·용량·전체 항목·참조 그래프 |
| `plan.json` | 삭제 **후보만** 추린 목록, inventory 해시, `readOnly=true`, `requiresFreshReview=true` |
| `report.md` | 용량·삭제 후보·전체 보존 사유·참조 파일과 대상 표 |
| `completed.json` | 세 보고서 파일의 SHA-256. 보고서 기록 완료 표시이며 원본의 전체 복구 검증 결과가 아님 |

`scanComplete=false`이면 읽기 실패·크기 제한·손상된 metadata·동시 변경 등의 사유를 `issues`에 기록하고 후보를 모두 제외한다. 정책 파일도 조사 전 내용과 조사 후 파일 상태를 확인한다. 명령의 정상 종료는 미리보기 생성 성공이므로 자동화에서는 `scanComplete`도 확인한다. 이 경우 자료를 읽을 수 있고 실행이 멈춘 조건에서 새 출력 경로로 다시 조사한다.

## 분류와 보호 순서

| 분류 | 의미 |
| --- | --- |
| `protected` | 측정/실행 원본·고정 빌드·실패 증거·참조 또는 명시적 보존 사유가 있음 |
| `review` | 임시 정책에 없는 미분류 자료이거나 스캔이 불완전함. 보존하며 수동 검토 |
| `candidate` | 임시 정책에 지정되고, 보호 사유와 외부 참조가 없으며 스캔이 완료됨. 검토할 삭제 예정 항목 |

삭제 후보도 자동 삭제 승인이 아니다. 오래됐거나 크다는 이유만으로 후보로 만들지 않는다. 정리 시점에는 다른 프로세스의 새 출력/참조와 정책 변경을 포함해 다시 조사해야 한다.

- `protocol.json`, `artifact-manifest.json`, `build.json`, `producer-build.json`, `failed.json`이 있는 단위는 원본/고정 빌드로 보호한다. 내부 Genesis/WAL·raw samples, demo/run summary도 보호한다.
- 요청 파일·실행 데이터·archive와 receipt·기존 cleanup audit는 보호한다. 현재/이전 실행의 실패와 중단을 성공으로 덮어쓰거나 후보로 바꾸지 않는다.
- CI는 mode별 최신 실행과 실패/미완료 실행을 보호한다. 실패 로그도 보존한다. 성공한 과거 CI라도 문서/다른 metadata가 인용하면 보호한다.
- diagnostic `manifest.json`을 가진 단위는 봉인 원본으로 보호한다. `report.json`, `report.md`, `qualification.json`만 담은 파생 보고서 manifest는 멤버의 현재 크기/해시가 일치할 때만 일반 분류를 적용한다.
- 측정 묶음은 통째로 하나의 단위다. 내부 파일을 따로 후보로 만들지 않는다. `ci`, `demos`, `diagnostics`, `comparisons`, `requests`, `runs`, `archives`, `cleanup`, `artifact-reports`만 바로 아래 항목을 개별 단위로 조사하고 다른 폴더는 전체를 유지한다. 이 보관용 폴더 자체에 protocol/manifest·고정 빌드·실행 데이터 등의 표시가 있으면 폴더 전체를 하나로 보호한다.
- symlink를 따라가지 않으며 해당 단위를 보호한다. 읽지 못한 참조 파일이나 조사 중 변경이 발견되면 전체 후보를 제외한다.

## 참조 범위

Git의 tracked 파일과 현재 nonignored source, `var`의 bounded text metadata를 읽는다. Markdown/JSON/text/log/Python/shell/Go/YAML 등의 **구체적 `var/...` 경로**와 현재 checkout의 절대 경로, `{portable,native}` 같은 제한된 경로 brace 축약, metadata SHA-256 인용을 연결한다. 경로 없는 `rawReportSHA256` 인용도 현재 metadata의 해시와 대조한다. build 경로와 `resumeFrom.path`가 이전 실행을 가리키면 참조 그래프에 표시한다.

참조 대상 단위는 참조하는 파일이 다른 곳에서 인용되지 않았더라도 보호한다. 따라서 이전 실행의 부모/조부모, 순환 참조도 보존되며 정리 가능 용량을 과장하지 않는다. 보고서에는 본문을 복사하지 않고 참조 파일·대상 단위·path/sha256 방식만 기록한다.

메타데이터는 파일당 최대 8MiB·전체 최대 256MiB까지 읽는다. raw JSONL 표본·WAL·바이너리·압축 member 내용은 참조 검색 대상이 아니다. 전체 checksum/재생 검증은 기존 [실험 검증 도구](diagnostic-tools.md)를 사용한다. 임의의 shell 식, bare basename, 다른 checkout의 절대 경로, wildcard glob은 역사적 근거의 구체적 참조로 해석하지 않는다. 이러한 형식이나 아직 알려지지 않은 자료의 의존성이 있다면 정책 `keep`에 경로를 추가한다.

출력 보고서가 다음 스캔에서 모든 자료를 영구 보호하지 않도록, `var/artifact-reports` 아래에서 이 도구의 네 파일·형식 식별값·완료 해시가 일치하는 보고서는 **참조 스캔에서 제외**한다. 추가 metadata가 섞이면 일반 자료로 조사하고, 완료 해시가 손상되면 스캔 제한으로 후보를 제외한다. 정책 파일의 경로 패턴도 증거 인용과 구분한다.

## 보존·임시 정책

기본 정책은 [config/artifact-retention.json](../config/artifact-retention.json)이다. `keep`은 추가 보존 glob, `temporary`는 후보 검토를 허용하는 glob이다. 입력·보호 규칙이 정책보다 우선하므로 넓은 임시 패턴으로 측정 원본이나 고정 빌드의 보호를 해제할 수 없다.

```json
{
  "version": 1,
  "keep": [],
  "temporary": ["var/ci/*", "var/artifact-reports/*", "var/.DS_Store"]
}
```

별도 정책은 `--policy`로 지정한다. 패턴은 **개별 파일이 아니라 표에 나타난 단위 경로 전체**와 대조한다. 이름이 비슷하다는 이유만으로 일반 실험 폴더를 임시 정책에 넣지 않는다. 기본 정책에 없는 오래된 로그·스크립트·빌드 확인 자료는 검토 필요로 유지한다.

용량은 정규 파일의 논리 크기와 `st_blocks × 512` 할당 크기를 구분한다. 전체 할당 합계는 inode 중복을 제외하며, 항목별 합계/후보 합계에는 공유 inode가 겹칠 수 있다. APFS clone·공유 블록 때문에 **실제 삭제 후 확보할 공간을 보장하지 않는다**. filesystem 여유 공간은 실행 시점의 별도 관측이다. 출력 파일은 조사 후 생성되므로 그 출력 자체는 해당 시점 inventory에 포함되지 않는다.

## 검증

```sh
python3 -m unittest discover -s tools/diagnostics -p 'test_artifacts.py' -v
bash scripts/ci.sh portable
bash scripts/ci.sh native
```

회귀 검사는 임시 Git 저장소에서 경로/brace/hash와 재개/순환 참조, 봉인 원본과 파생 보고서, 최신/실패 CI, symlink·읽기 실패·입력 변경·용량과 실제 명령 출력을 확인한다. 공용 CI의 Python discover에 포함되며 대용량 실제 var 스캔이나 새 부하 측정은 CI에서 수행하지 않는다.

2026-11-12 실제 조사와 기존 파일/해시 보존, 로컬 CI 및 마지막 경로 검증 회귀 결과는 [완료 계획](exec-plans/completed/20261112-artifact-retention.md)에 기록했다.

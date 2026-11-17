# 실험 자료 보존 범위

기준일: 2026-11-17. 사용자가 과거 실험 전체를 계속 보관하는 대신 최신 기준·직전 비교·중요 실패·고정 빌드를 남기고 정리하도록 승인했다. 아래 목록만 원본 보존을 종료했다. **과거 수치와 실패 판정은 요약으로 남지만, 삭제한 원본으로 해당 실험 전체를 재검증하거나 비교표를 재생성할 수 없다.** 최신 자료와 삭제된 자료를 혼합해 검증 완료로 표현하지 않는다.

## 그대로 보존하는 자료

- 최신 10분 `var/diagnostics/soak-latest-10m-20261116`과 직전 `var/soak-optimized-20261102.yawwm75d`: snapshot 끔/켬 전체 원본·반복/실패·고정 빌드 유지. 최신 미전송 3/11·UNKNOWN 1건·성능 미채택을 그대로 유지한다.
- 최신 정상 처리량의 `var/normal-capacity-*`, `var/normal-capacity-recheck-*`, `var/normal-capacity-build-20261107`: 누적 24회와 실패·중단·미완료 관측 및 판정 전체를 유지한다. 일부 성공 실행만 골라 보관하지 않는다.
- 현재 고정 상태 비교 입력과 최신 프로파일: `var/integer-json-full-20261113`, `var/canonical-buffer-full-20261113`, `var/snapshot-array-full-20261113`, `var/snapshot-tail-full-20261113`, `var/read-buffer-full-20261113`, `var/state-scale-full-20261113`, `var/wal-decoder-full-20261113`, `var/snapshot-leaf-full-20261113`, `var/post-optimization-profile-full-20261113`, `var/snapshot-read-profile-full-20261113`와 이들의 고정 빌드·이전 입력 의존성. 아래 목록에 없는 RSS/계측/진단 자료도 유지한다.
- 최신 거래·복구 데모, 무작위/UNKNOWN/동시 요청·SIGKILL·캠페인 원본과 기록 replay, 현재 CI 성공·실패·미완료 기록, 기존 패키지/복원 검증 근거. 이번 정리는 새 성능·전원 장애 증거를 추가하지 않는다.

## 보존을 종료한 원본

아래 할당량 합계는 약 **18.211GiB**다. 파일 할당량과 실제 filesystem 여유 변화는 APFS 공유 블록·다른 프로세스에 따라 다를 수 있으며 실제 관측은 [정리 기록](benchmarks/20261117-retired-experiments.json)에 별도로 남긴다. 각 경로의 raw WAL/snapshot/표본/trace/profile 및 archive 자체는 삭제했으므로 로컬에서 전체 복원할 수 없다.

| 삭제 경로 | 삭제 전 할당 GiB | 요약 보존과 대체 근거 |
| --- | ---: | --- |
| `var/soak-comparison-20261030.92490s` | 3.506 | 최초 10분 비교: 최신 11/16·직전 11/02 원본과 대체 명세 유지 |
| `var/controlled-baseline-reference` | 3.283 | 구형 ca74 통제 기준: 최신 정상 조건 누적 캠페인 전체 유지 |
| `var/p6-initial-dgzi6jmf` | 2.058 | 초기 P6 탐색: 요약·실패/설정 보존, 최신 처리량 캠페인 전체 유지 |
| `var/snapshot-profile-full-20261113` | 1.994 | 최초 snapshot 프로파일: 최신 읽기/최적화 후 profile·비교 상태 유지 |
| `var/diagnostic-soak-20261103.fz83p2f4` | 1.148 | 초기 진단 반복: 요약/로그 보존, 현재 장애 재현과 최신 10분 유지 |
| `var/snapshot-stages-20261030.ye3p3gdp` | 0.688 | 초기 snapshot 단계 계측: 요약/설정 보존, 최신 고정 상태 비교 유지 |
| `var/snapshot-preflight-20261102.ztj_vssr` | 0.627 | 구형 preflight 비교: 요약/설정 보존, 최신 고정 상태 비교 유지 |
| `var/snapshot-decoder-20261030.fq2sk1r9` | 0.626 | 구형 복구 decoder 비교: 요약/설정 보존, 최신 decoder 비교 유지 |
| `var/archives/p6-baseline-4gm7mz4t.tar.gz` | 1.578 | 초기 P6 기준 archive: 고정 빌드·설정/요약/receipt 별도 보존 |
| `var/archives/p7-final-pqepq5wq.tar.gz` | 0.289 | 초기 P7 archive: 고정 빌드·설정/요약/receipt 별도 보존 |
| `var/archives/p7-snapshot-capacity-6a5f8szg.tar.gz` | 0.156 | 초기 snapshot capacity archive: 고정 빌드·설정/요약/receipt 별도 보존 |
| `var/archives/p7-snapshot-sorting-kyynt605.tar.gz` | 2.259 | 초기 snapshot sorting archive: 고정 빌드·설정/요약/receipt 별도 보존 |

기존 Markdown 수치와 JSON/spec은 당시 기록으로 유지했다. 영향을 받는 문서에는 가용성 안내를 붙였고, [기계 판독 목록](benchmarks/20261117-retired-experiments.json)의 `historicalJSONDocuments`가 삭제 원본을 참조하는 JSON 문서를 표시한다. mixed 문서는 그 안의 모든 자료가 삭제됐다는 뜻이 아니다. 예를 들어 최신 10분 보고서는 최초 10/30 비교만 재검증할 수 없고 최신/직전 비교는 가능하다. 초기 archive의 기존 receipt는 checksum 이력이며 archive가 남아 있다는 증거가 아니다.

## 실제 정리 결과

8개 디렉터리와 4개 archive를 삭제했다. 삭제 직전 약 15.47GiB → 직후 관측 약 33.66GiB로 **약 18.20GiB 증가**했다. 선보존 자료는 삭제 직전 수치에 이미 포함돼 있다. 재집계로 생긴 재생성 가능한 Go build/test cache도 비웠고 최종 관측 여유는 약 33.78GiB다. 이 값은 시점 관측이며 다른 프로세스의 사용량 변화를 분리한 보장값은 아니다.

삭제 후 최신/직전 10분 두 그룹은 기존 비교 JSON과 정확히 일치했고, 최신 정수 JSON 비교 60 worker 및 최신 캠페인/기록 replay/장애 자료를 포함한 11개 봉인 묶음(2,248파일)을 검증했다. 기존 보존 파일 40,785개와 기존 비Markdown 소스/설정 368개는 그대로였다. 정리 중 바뀐 root/cleanup의 Finder metadata 2개만 별도 복사·기록했다. 기존 Markdown 129개의 본문은 유지하며 가용성 안내만 추가했다. 전체 코드 CI는 문서/로컬 자료만 변경한 이번 작업에서 다시 실행하지 않았다.

## 남긴 요약·고정 빌드 찾기

Git 제외 `var/history-retired-20261117`에 다음을 보존했다. 이 폴더도 삭제하면 Git의 문서/가용성 목록만 남는다.

- `original-files.jsonl`: 삭제 대상 파일별 원래 경로·크기·SHA-256·파일 identity. archive는 압축 파일 hash와 receipt로 member 목록/크기를 검증했다.
- `preserved-index.json`: 원래 경로(archive는 `archivePath!/memberPath`)에서 `sha256`/원래 mode/분류로 연결한다. `blobs/<sha256>`에 동일 bytes는 한 번만 저장한다. 파일을 찾을 때 index의 원래 경로로 검색하고 필요한 blob만 새 경로로 복사한다. 바이너리 실행이 필요하면 index의 mode를 참고해 복사본에 적용한다.
- 설정·요약·기존 manifest/receipt·소스 메모·작은 텍스트 로그·고정 실행 파일을 2,236개 경로의 1,104개 blob, 약 209.7MiB로 보존했다. 고유 Go 실행 파일 26개의 SHA와 `go1.27.3` header를 확인했으며 구형 파일을 실행하지 않았다.
- `manifest.json`/`completed.json`: 보존 파일의 봉인. `PRESERVATION_VERIFIED`와 `rawInputsRetired=false`는 **삭제 전 선보존 단계**의 기록이다. 최종 삭제·가용성 상태는 [별도 목록](benchmarks/20261117-retired-experiments.json)과 `var/cleanup/20261117-historical-retirement.json`을 따른다. 봉인을 바꾸거나 원본 전체 보관으로 해석하지 않는다.

요약·바이너리만으로 삭제한 표본/거래 데이터의 완전 복원이나 과거 수치 재검증은 할 수 없다. 봉인 검증 도구로 보존 blob의 무결성은 확인할 수 있다.

## 남은 자료의 재집계

저장소 루트에서 현재 보존한 두 10분 비교 그룹만 재생성한다. 기존 4그룹 명세를 그대로 실행하면 삭제한 10/30 입력을 사용할 수 없다. 출력은 매번 새 경로를 사용한다.

```sh
python3 tools/diagnostics/experiment.py compare \
  --spec docs/benchmarks/20261117-retained-soak-spec.json \
  --output var/comparisons/retained-soak-review-001
python3 tools/diagnostics/integer_json_compare.py analyze \
  --bundle var/integer-json-full-20261113 \
  --output var/integer-json-retained-review-001
```

두 10분 비교 그룹은 당시 JSON의 해당 그룹과 수치·조건·판정이 정확히 일치했다. 최신 정수 JSON 비교는 60 worker를 재검증했다. Finder의 추가 `.DS_Store`가 최초 재집계를 막아 원본 봉인 밖 `var/cleanup/20261117-history-finder-metadata.bin`과 `var/cleanup/20261117-history-finder-extras`에 보존했다. 실패한 최초 보고서도 감사 이력으로 남겼으며 측정 파일/manifest는 수정하지 않았다.

실제 **OS·전원 장애 검증은 미완료·보류**, v1 전체 계획은 **Blocked**다. 이번 정리는 문서와 로컬 원본 보관만 변경하며 새 부하 측정이나 commit/merge/push를 포함하지 않는다.

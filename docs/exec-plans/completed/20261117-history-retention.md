# 과거 실험 원본 보존 범위 축소

상태: Completed
시작일: 2026-11-17
종료일: 2026-11-17

## Purpose / Acceptance

사용자가 최신 기준·직전 비교 전체, 중요한 실패 재현·고정 빌드/설정을 남기고 과거 예비/중복 실험은 요약 보존 후 정리하도록 요청했다. 단순한 참조 없는 임시 자료 정리와 달리, 명시한 과거 원본의 보존 종료를 허용한 요청이다. 최신 11/16·직전11/02 10분 자료, 최신 처리량 캠페인의 반복/실패 전체와 현재 고정 상태 비교·최근 기능/장애/CI 자료를 유지한다. 삭제 대상의 설정/요약/기존 봉인·파일별 hash와 고정 바이너리를 먼저 보존하고 문서에 원본 재검증 불가를 표시한다.

## Progress

- [x] 현재 작업 트리·보관 자료/문서와 사용자 승인 범위 확인, 기존 미커밋 구현 보존.
- [x] 과거 원본 삭제 목록 확정·고정 빌드/설정/요약 보존·전체 파일 inventory/hash.
- [x] 최신+직전 비교의 별도 재집계 명세와 보존 검증.
- [x] 삭제 대상 문서 가용성 표시·승인된 원본만 정리·실제 공간 확인.

## Surprises & Discoveries

과거 tar.gz에는 원본 표본뿐 아니라 당시 고정 exchange/loadgen도 포함돼 있다. 고정 바이너리를 먼저 content hash 단위로 보존해 동일 bytes 복사본을 모은 뒤 원본 archive만 정리한다. 삭제 대상에 문서 참조가 있어도 요약 수치를 지우거나 과거 성능 실패를 통과로 바꾸지 않는다. 최신10분의 기존4그룹 명세에는10/30이 포함돼 있어, 보존한11/02 대비11/16 두 그룹만 선택하는 명세가 필요하다.

## Decision Log

- 2026-11-17: 앞선 참조 없는 자료 정리 승인과 달리 이번 요청은 선택한 과거 인용 원본의 보존 종료까지 포함한다. 자동 보관 도구의 보호 규칙을 낮추거나 과거 수치를 바꾸지 않고 별도 가용성 목록/수동 계획으로 처리한다.
- 2026-11-17: 8개 과거 디렉터리와4개 초기 P6/P7 archive를 검토한다. 최신 원본과 고정 상태 비교 의존성·고정 빌드·현재 실패 재현은 삭제 범위 밖이다. 실제 OS·전원 장애 검증은 미완료·보류, v1 Blocked 유지.

## Outcomes & Retrospective

8개 과거 원본 디렉터리·4개 archive를 삭제했다. 약18.211GiB 할당량 중 설정/요약/고정 빌드는1,104 blob(약209.7MiB)으로 선보존·봉인했다. 삭제 직전/직후 여유 공간은15.47→33.66GiB(약18.20GiB 증가), 캐시 정리 뒤 최종 관측33.78GiB다. [가용성 목록](../../benchmarks/20261117-retired-experiments.json)과 [보존 안내](../../experiment-retention.md)에 원본 재검증 불가·현재 가능한 두 그룹 명세를 기록했다. 기존 source/설정368개·보존 파일40,785개와 기존 Markdown129개 본문을 유지했다. 소스 코드 변경·새 부하 측정·commit/merge/push는 수행하지 않았다. 실제 OS·전원 장애 검증은 미완료·보류, v1 Blocked다.

## Validation and Acceptance

삭제 전에 개별 파일 hash·metadata identity와 요약/설정/바이너리 보존을 검증한다. 보존한 최신+직전 10분 비교, 최신 기능/장애/캠페인 봉인과 고정 상태 자료의 검증/재집계 가능 범위를 확인한다. 삭제 뒤 보존 파일 identity와 기존 코드/미커밋 문서 내용 유지·가용성 표시·실제 디스크 증가를 기록한다. 숫자/판정은 과거 요약으로 유지하며 삭제된 원본을 재검증 가능하다고 주장하지 않는다.

## 실제 검증 기록

- 삭제 전후 `experiment.py compare --spec docs/benchmarks/20261117-retained-soak-spec.json`: 두 그룹의 전체 관측/조건/판정이 기존 4그룹 보고서의 첫 두 그룹과 정확히 일치. 최신 성능 무효·UNKNOWN1 유지.
- 삭제 전후 `integer_json_compare.py analyze --bundle var/integer-json-full-20261113`:60 worker 검증 통과. 최신 기능/장애/캠페인과 보존 blob 총11묶음/2,248파일 봉인 검증 통과.
- Finder가 봉인 뒤 `.DS_Store`를 추가해 최초 strict inventory 검증이 실패했다. 모든 예외가 Finder metadata뿐인지 확인해 `var/cleanup`에 별도로 보존하고 원래 측정 파일/manifest는 수정하지 않았다. 정리 중 root/cleanup metadata2개의 변경도 기록했고 측정 파일 변경은0개다.
- 고유 Go 고정 binary26개의 hash/Go1.27.3 header 검증, archive hash/receipt member 대조, 전체 원본 파일3,481개 inventory/hash 확인 후 삭제했다. 마지막 Finder metadata 추가/변경은 별도 감사 inventory로 남겼다.
- `scripts/go clean -cache -testcache` 완료. 보존 자료는 캐시 정리 대상이 아니다. 감사 기록: `var/cleanup/20261117-historical-retirement.json`. 문서/로컬 자료 작업이므로 코드 테스트/전체 CI 재실행 생략.

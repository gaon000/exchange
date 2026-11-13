# 상태 크기별 메모리·snapshot·복구 비용

상태: Completed
종료일: 2026-11-13

## Purpose / Big Picture

1,000/5,000/10,000개 고유 명령의 활성 주문형·체결/취소 이력형을 준비한다. 6개 상태 조건마다 동일 fixture에서 새 프로세스·새 사본으로 5회씩 WAL 전체 복구와 snapshot 복구를 수행한다. WAL 복구 뒤 새 snapshot 생성을 측정하며 완전한 canonical snapshot bytes와 원본 WAL/Genesis/lock 불변을 확인하고 JSON/Markdown 중앙값·최소/최대 표를 만든다. HTTP 부하·최대 처리량·전원 장애 실험은 아니다.

## Progress

- [x] 현재 소스·기존 stage 계측·복구 계약과 디스크 여유 확인, feature branch 생성.
- [x] 제한된 deterministic fixture/probe·새 프로세스 계측/검증·원본 재집계 도구 구현.
- [x] 회귀·작은 smoke·빌드/소스 동결과 6조건 5회 본 측정.
- [x] 상태/해시 대조·JSON/Markdown·분석·전체 CI/문서 검증.

## Decision Log

- 2026-11-13: 거래 production 코드는 수정하지 않는다. 별도 Go probe와 Python 표준 라이브러리 조정/집계를 사용하고 실험 바이너리·소스·조건을 출력에 동결한다.
- 2026-11-13: 활성형은 비교차 가격 두 단계의 지정가를 쌓는다. 이력형은 매도 2→매수 1 부분 체결→잔량 취소→체결된 매수 취소 거절의 4명령 사이클을 반복해 활성 주문을 0으로 만든다. 명령 수는 같지만 주문/체결 구성은 다르므로 단일 원인 대조로 해석하지 않는다.
- 2026-11-13: fixture는 배치 WAL 생성+순차 엔진으로 준비한다. 준비/파일 사본/해시/강제 GC/후속 검증은 시간 측정 밖이다. worker는 매번 새 프로세스이며 cache flush를 하지 않는 warm-cache 반복이다. 반복별 상태/복구 모드 순서를 교대한다.
- 2026-11-13: Go HeapAlloc/TotalAlloc/GC와 Darwin RUSAGE_SELF ru_maxrss(bytes)를 기록한다. Open 뒤 강제 GC의 live heap, snapshot 할당량, 검증 전까지의 누적 RSS high-water를 구분한다. RSS 차이를 개별 phase peak로 간주하지 않는다.
- 2026-11-13: 정상 snapshot 복구는 tail=0이지만 전체 WAL prefix를 검사하는 현재 계약을 유지한다. snapshot 복구 뒤 같은 파일 재확인 publication은 새 생성과 별도 기록한다.

## Surprises & Discoveries

이전 최적화는 주로 같은 51,385 명령 상태 하나의 전후 비교였다. 이번은 독립적인 작은 크기별 비용 측정이며 과거 wall-time을 현재 baseline으로 재사용하지 않는다. 오래된 Apple archive man page의 maxrss 단위 설명과 현재 macOS man page가 달라, 현행 Darwin source/man의 bytes 정의를 확인했다. 시점별 수치를 혼합하지 않는다.

## Validation and Acceptance

여유 8GiB 보존·묶음 4GiB 상한·최대 10,000명령·5반복을 명시한다. 준비된 상태 구성/정수 기대값·환경/프로토콜·고정 입력 해시, 작은 실제 native/미지원 검증과 반복 수/중복/오류/누락 거절을 검사한다. 30쌍/60 worker의 복구 report·완전한 snapshot/WAL/Genesis/lock 해시 일치를 확인한다. 원본을 다시 검증하고 report를 재생성한다. 전체 CI는 짧은 smoke만 사용하고 본 측정과 분리한다.

## Outcomes & Retrospective

제한된 Go fixture/probe와 Python 조정·원본 재검증/집계, Go/Python 회귀·native CI의 작은 새 프로세스 smoke를 구현했다. production 거래 코드는 수정하지 않았다. 6조건×5회×두 복구 경로의 60 worker 모두 전체 canonical snapshot/WAL/Genesis/lock bytes를 확인했고 원본 사본과 새 보고서의 검증을 마쳤다.

10,000 명령에서 WAL 복구 후 heap 증분 중앙값은 active 10.136MiB/history 8.957MiB, 새 snapshot은 62.975/55.755ms, WAL Open은 197.738/171.015ms, snapshot Open은 428.841/358.417ms였다. 상태 구성과 cache/GC·단계별 근거/최소~최대를 함께 보존한다. 현 계약에서 snapshot도 보존 WAL 전체를 검사하며 load/rebuild/확인 비용이 남아, 이번 tail=0 조건에서 snapshot 복구가 약 2.10~2.17배 오래 걸렸다. 이를 cold-cache/HTTP p99/최대 처리량이나 개선 성과로 확장하지 않는다.

별도 장비 전원 장애는 미완료·보류다. 구현 완료 시점의 위 검증은 로컬 결과이며, 이후 커밋·병합·푸시와 GitHub 원격 CI 결과는 구분해 확인한다.


실제 검증:

- Go pure 회귀 2개(상태 구성·독립 정수 잔고/예약 기대값, 입력 범위·출력 재사용), native 실제 fixture/두 복구/소유권/변조 검증 1개와 unsupported 무변경 1개를 추가했다. native race와 civerify 검증은 `var/state-scale-native-tests-20261113.log`에 보존했다.
- 새 Python 회귀 5개는 60개 선언 순서/반복 및 느린 표본 유지, read-only 재집계, 누락/중복·순서·상태 유형/구성·원본/binary 변경, 검증 실패/GC/메모리, 자원 예산/원본 보존을 확인했다. 전체 Python 58개 통과(`var/state-scale-python-20261113.log`).
- 본 측정 전 `bash scripts/ci.sh portable`: 224 tests/23 packages, Python 58개·diagnostic package restore smoke·포맷/vet/build 통과. `var/state-scale-ci-portable-20261113.log`, `var/ci/portable.GGO8Ey`.
- 본 측정 전 `bash scripts/ci.sh native`: race 261 tests/23 packages, 명시적 child-helper skip 1개, Python 58개와 12명령/4worker 실제 비용 smoke·native HTTP/trace/I/O/package restore 통과. `var/state-scale-ci-native-20261113.log`, `var/ci/native.IBnCRP`.
- 본 측정: `python3 tools/diagnostics/state_scale.py run --output var/state-scale-full-20261113`. 로그 `var/state-scale-full-20261113.log`; 같은 root에 코드 229개 동결 source archive와 바이너리·장비/조건·fixture 6개·worker 60개의 raw JSON/사본·report.json/report.md·봉인을 보존했다. 총 395,843,403 논리 bytes(약 377.5MiB), 588개 파일 무결성 검증 통과. 기존 원본은 수정/삭제하지 않았다.
- 재집계: `python3 tools/diagnostics/state_scale.py analyze --bundle var/state-scale-full-20261113 --output var/state-scale-reanalysis-20261113`. 새 보고서 JSON/Markdown의 SHA-256이 본 측정 보고서와 같고 각 metric count=5다.
- 마지막 독립 검증 `var/state-scale-final-proof-20261113.json`: 60개 worker/6조건/모드별 5회, 원본 파일 봉인·source archive 229개 member의 SHA-256과 현재 source 해시 일치, 동결 바이너리와 보고서 재생성 일치로 PASSED다.
- [결과 보고서](../../benchmarks/state-size-costs.md), [JSON](../../benchmarks/20261113-state-size-costs.json), [원본 해시/fixture index](../../benchmarks/20261113-state-size-cost-artifacts.json), [도구/계측 계약](../../state-size-costs.md)에 연결한다. Markdown 경로·JSON·새/추적 파일 공백과 포맷을 확인한다.

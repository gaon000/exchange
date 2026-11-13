# 스냅샷 생성·복구 CPU·할당 프로파일링

상태: Completed
종료일: 2026-11-13

## Purpose / Big Picture

기존 1만 명령 active/history fixture bytes를 보존하며 WAL Open, snapshot Open, 새 snapshot 생성의 CPU·할당 경로를 분리한다. 계측 실행은 기존 비계측 시간 측정과 혼합하지 않는다. production 최적화는 이번 범위에 포함하지 않으며 근거를 바탕으로 후속 후보를 선택한다. 실제 전원 장애 검증은 미완료·보류다.

## Progress

- [x] 현재 코드·기존 계측/원본·디스크 확인, feature branch 생성.
- [x] 제한된 새 프로세스 profile probe·실행/집계 도구와 회귀 검증.
- [x] 동일 상태 CPU/할당 수집과 원본/복구 bytes 대조.
- [x] 함수별 근거·한계·후속 후보와 재현 명령 기록.

## Decision Log

- 2026-11-13: Go 표준 pprof/Go tool pprof만 사용하며 dependency를 추가하지 않는다. CPU와 allocation은 서로 다른 worker다. allocation은 64KiB 샘플링, 두 GC 후 baseline/after 차이를 사용하고 internal 경로로 focus해 profile 직렬화/준비 비용을 구분한다.
- 2026-11-13: 같은 fixture의 사본/새 worker에서 CPU Open 각 20회·create 각 40회, allocation 각 5회 수집한다. CPU 표본 수를 함께 보고하며 추정된 순위와 wall-time 비중을 혼동하지 않는다. 전체 4GiB/디스크 여유 8GiB 예산을 유지한다.

## Validation and Acceptance

probe 범위/소유권·기존 파일 거절, 실제 native 각 경로/각 profiler의 profile 파일/복구 상태를 확인한다. Python 집계는 fixture/worker/profile/동결 binary와 source 해시·실행 수를 검증한다. 원본 전체 봉인을 전후 대조하고 새 보고서를 재생성한다. 관련 회귀와 전체 로컬 CI를 수행한다.

## Outcomes & Retrospective

동일 1만 명령 active/history의 190 worker가 복구 report·완전한 canonical snapshot/원본 입력 해시 검증을 통과했다. 새 폴더의 보고서 재생성 bytes 일치와 source 233개 archive/현재 해시·이전 production 155개 일치를 확인했다. 복구의 WAL decodeRecord/strictjson 반복 검증, 생성의 leaf JSON 임시 bytes와 I/O 호출 스택을 구분했다. 다음 후보는 WAL 전용 canonical decoder이며 이번에는 production 최적화를 구현하지 않았다.

검증 중간 기록: Go native profile 회귀와 12 worker 최종 smoke, Python 61개, portable 224개·native race 262개/각 23 packages 및 진단 smoke 통과. portable 최초 sandbox 실행은 loopback bind 권한으로 실패했고 별도 권한 재실행에서 통과했다. 장비 sysctl sandbox 실패도 보존했다. 본 측정은 검증 종료 뒤 별도 실행으로 수집했다.


## 최종 근거와 한계

- 본 자료 `var/snapshot-profile-full-20261113`의 2,126개 파일과 입력 `var/state-scale-full-20261113`의 588개 파일 봉인을 검증했다. binary SHA `b908d7211bbefe2eff4150b2e3814f7036616a30b4acac07d9044ebb96d6d35d`, source archive 233개, 이전과 같은 production 155개를 확인했다.
- CPU Open 각 20회/create 각 40회와 allocation 각 5회는 다른 worker다. warm-cache·비격리·tail 0 조건이며 CPU sample/샘플링 allocation을 비계측 latency/처리량으로 해석하지 않는다. 전역 GC/비동기 scavenger·profile 부수 비용도 보존하고 internal focus를 구분한다.
- `var/snapshot-profile-reanalysis-20261113`에서 새 보고서 JSON/Markdown bytes가 원본과 같다. 별도 syscall peek/traces는 `var/snapshot-profile-callers-20261113`에 봉인했고 최종 proof는 `var/snapshot-profile-final-proof-20261113.json`이다.
- [분석 결과](../../benchmarks/snapshot-profiling.md), [요약 JSON/해시](../../benchmarks/20261113-snapshot-profiling.json), [실행·해석](../../snapshot-profiling.md)에 명령·함수별 근거·후속 후보/거절 계약·실패 보존을 기록했다.
- 실제 전원 장애는 미완료·보류, v1 전체 Blocked를 유지한다. 위 구현 완료 시점의 검증은 로컬 결과이며, 후속 커밋·병합·푸시와 원격 CI 결과는 따로 확인한다.

## 마스터 반영 전 검토

출력 생성/빌드 전의 준비 공간 검사와 각 fixture 복사 전의 실제 크기 예산 검사를 추가했다. 처음에는 이 검사가 fixture 복사 뒤에 있어 준비 중 8GiB 여유 기준을 침범할 수 있었다. 공간 부족 시 출력/빌드를 만들기 전에 거절하고 원본을 유지하는 회귀를 추가했다. 기존 190개 profile의 probe·production bytes와 원본은 그대로 유지했다. 소스 archive 233개는 수집 종료 시점 코드이며, 이후 바뀐 파일은 조정기와 해당 Python 회귀 두 개다.


검토 수정 후 `var/ci/portable.C71fFA`·`var/ci/native.GfKd1n`에서 전체 CI를 다시 통과했다. Go portable 224개·native race 262개/23 packages(기존 child-helper skip 1개), 각 Python 62개와 진단 smoke다. `var/snapshot-profile-review-ci-portable-20261113.log`, `var/snapshot-profile-review-ci-native-20261113.log`와 작은 12 worker `var/snapshot-profile-review-smoke-20261113`에 보존했다. 기존 자료 2,126개/입력 588개 봉인, archive 233개와 요약 JSON 해시를 다시 확인했고 `var/snapshot-profile-review-reanalysis-20261113`의 report.json/report.md가 기존 보고서와 bytes까지 같다.

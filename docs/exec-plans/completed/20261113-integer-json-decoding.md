# 정수 JSON 디코딩 임시 할당 최적화

상태: Completed
종료일: 2026-11-13
시작일: 2026-11-13
기준: dc7f10f와 이전 작업의 미커밋 profile 입력 검증·보고서 변경을 보존한다.

## Purpose / Acceptance

[최신 프로파일](../../benchmarks/post-optimization-profiling.md)에서 읽기 할당 중 Int.UnmarshalJSON 자체 active3.238/history2.450MiB가 확인됐다. 일반 quoted ASCII digits를 임시 JSON string 없이 ParseInt하고 whitespace/escape 등은 이전 JSON parser로 넘긴다. 공개 오류 분류/실패 시 receiver·입력 보존/정수 경계·WAL와 snapshot canonical/복구 계약을 바꾸지 않는다. 새 의존성은 추가하지 않는다.

두 frozen build는 integer.go만 생산 차이를 허용하고 Go probe는 같게 유지한다. 최신 canonical-buffer candidate와 같은 active/history 각1만 명령 자료·tail0/1천/5천 조건을 각 버전5회·총60개 새 프로세스에서 비교한다. TotalAlloc/Open wall/GC 뒤 heap·현재 RSS/누적 peak RSS와 전체 bytes·최초8응답/중복 재시도2건·후속 체결/취소·예약 반환을 검증한다. profiler는 끄고 기존 2GiB 메모리 budget·4GiB 묶음/별도8GiB 여유·128MiB 준비 gate를 지킨다. 원본·실패·고정 빌드를 보존한다.

## Progress

- [x] 현재 브랜치/이전 변경·최신 profile/정수 공개 계약 확인, 기능 브랜치 생성.
- [x] 독립 legacy oracle 차등/퍼즈·microbenchmark, 일반 입력 경로 구현.
- [x] baseline/candidate 동결·작은12 worker·같은 상태60 worker 비용/복구 검증.
- [x] JSON/Markdown·새 폴더 재집계·전체 로컬 CI·채택 판정/문서 갱신.

## Surprises & Discoveries

- domain Int는 snapshot 외 HTTP·WAL에도 공유한다. direct method와 json.Unmarshal 호출 모두 비교하고 escape/공백·오류 우선순위를 유지해야 한다.
- 첫 microbench가 fuzz와 겹쳐 해당 로그를 보존하고 fuzz 종료 후5회 전부 새로 측정했다. 첫 oracle의 JSON 경유 수신자를 pointer로 수정한 뒤 기존/후보가 모두 통과했다.
- 기존 배열 비교 runner의 fixture producer는 최신 코드보다 오래됐다. producer 검증을 canonical-buffer candidate까지 확장하고 허용 생산 차이는 명시적으로 integer.go 하나만 지정한다.

## Decision Log

- 2026-11-13: 1~19자리 quoted ASCII digits만 fast path로 처리한다. 먼저 모든 byte가 숫자인지 확인한 뒤 기존 ParseInt를 재사용해 syntax 오류와 overflow의 우선순위를 보존한다. 나머지 입력은 기존 JSON decode에 위임한다.
- 2026-11-13: 이전 profile 도구/보고서는 덮어쓰지 않고 미커밋 변경을 함께 보존한다. 이번 요청은 구현/로컬 검증이며 커밋·master 병합/푸시·원격 CI는 포함하지 않는다. 실제 전원 장애는 미완료·보류다.

## Outcomes & Retrospective

60 worker/327개 봉인 파일·archive 각273개 source를 확인했고 생산/전체 source 차이는 integer.go 하나다. 최초480응답/재시도120건·후속 체결/취소/예약 반환·전체 복구 bytes가 일치했다. TotalAlloc 중앙값2.90~4.22%, Open wall7.37~9.01% 감소(각30/30쌍 감소)로 채택했다. live heap은 active감소/history7.83~21.67KiB 증가, RSS 중앙값6조건 감소/개별9쌍 증가여서 메모리 일괄 개선은 주장하지 않는다. [최종 보고서](../../benchmarks/integer-json-decoding.md)에 원본/환경·모든 표본·한계를 남겼다.

정수 마이크로벤치 일반 숫자는16B/1alloc→0이며 fallback24B/2alloc를 유지했다. 30초 fuzz3,196,674건·독립 oracle 차등·portable241개/23packages/native race280개/23packages(기존 child-helper skip1개), 각 Python103개·진단/HTTP/복구 smoke가 통과했다. 새 폴더 재집계 JSON/Markdown은 bytes까지 같고 기존 resident array/canonical full의 공유 검사기 재집계도 동일했다.

첫 oracle 수신자 오류와 겹친 첫 microbench/과거 schema 선택 검사 로그를 보존하고 바로잡은 결과를 별도로 기록했다. 전후 build/fixture/실패를 삭제하지 않고 성공 scratch만 검증 receipt로 대체했다. 근거는 var/integer-json-* 및 var/ci/portable.ULWU7D·native.y5R7pO다.

구현·로컬 검증 완료 시점에는 이전 profile 변경을 포함한 기능 브랜치의 미커밋 상태였다. 이후 커밋/푸시·원격 CI는 별도 발행 단계에서 확인한다. 비격리 warm-cache 고정 상태 실험이며 HTTP p99/지속 처리량 개선이나 실제 전원 장애를 검증하지 않는다. 실제 전원 장애는 미완료·보류, v1 전체 Blocked 유지.

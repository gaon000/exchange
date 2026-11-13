# 스냅샷 항목 직렬화 임시 할당 최적화

2026-11-13, 항목별 json.Marshal의 반환용 임시 bytes를 재사용하는 json.Encoder 경로로 대체했다. **같은 1만 명령 active/history 상태에서 새 생성 전후 각각 5회, 두 복구 경로 포함 총 40 worker의 전체 bytes 일치를 확인했다.** 생성 할당 중앙값이 약 32.64%/34.28% 줄어 후보를 채택한다. 생성 시간 중앙값은 낮아졌지만 전후 범위가 겹치고 더 느린 후보 쌍도 있어 안정적인 생성 속도 향상이나 HTTP 지연/처리량 개선은 확정하지 않는다.

[동결·실행·재집계](../snapshot-leaf-comparison.md), [JSON](20261113-snapshot-leaf-comparison.json), [원본/빌드/fixture와 개별 쌍](20261113-snapshot-leaf-artifacts.json)을 함께 읽는다. 전원 장애는 **미완료·보류**다.

## 변경과 계약 유지

- 생산 변경은 `internal/snapshot/codec.go` 하나다. payload마다 표준 json.Encoder를 한 번 만들고 leaf마다 Encode한다. 표준 라이브러리 내부 버퍼를 writer 호출 중에만 소비해 Marshal 반환 bytes 사본을 없앤다. 필드 선언 순서/omitempty·HTML/UTF-8 escaping·정수 문자열·nullable Reason·custom Marshaler는 표준 라이브러리가 그대로 처리한다. escaping 코드를 새로 구현하지 않았다.
- adapter는 표준 Encoder가 붙인 마지막 framing 개행 하나만 제거한다. JSON 문자열 내부의 escaped newline은 유지한다. 개행 제거 뒤 16KiB leaf 상한을 검사하고, 완전한 JSON 값이 생성되기 전에는 실제 leaf writer를 호출하지 않는다. writer의 오류/short write도 전파한다.
- 기존 shape·크기 사전 계산·오류 우선순위와 파일 접근 전 16KiB/2GiB 검사는 유지한다. 표준 Marshal와 오류 종류/문구·도메인 sentinel 전파, 잘못된 UTF-8 대체 bytes까지 차등 검사했다. 독립 golden 전체 envelope bytes·길이/header/SHA-256은 같다.
- 전체 상태를 하나의 JSON buffer로 만들지 않고 기존 항목/배열 streaming을 유지한다. owner 사본/정렬과 파일 sync→exclusive rename→directory sync, 동일 파일 확인/복구·fallback 및 WAL 입력 대조는 변경하지 않는다. per-payload Encoder를 공유하지 않으므로 별도 runtime 간 가변 버퍼를 공유하지 않는다.
- 비교 도구는 기존 WAL 조정기에 허용 생산 파일/Markdown 렌더러 인자를 추가해 재사용한다. 기본 WAL CLI 동작·허용 경로/출력은 유지하며 이전 보관 WAL report의 재생성 bytes도 같음을 확인했다.

## 조건과 원본

기준 master는 WAL 디코더 최적화를 포함한 `dc8635fb3f5901efa263edc00b8b1a00ff4bb859`다. baseline은 생산 코드 변경 전, candidate는 검증한 작업 트리에서 동결했다. 둘 다 revision은 같고 dirty 상태이며, 동결 source/binary hash로 구분한다. baseline은 조정기/문서 변경으로 dirty였고 생산 코드는 해당 HEAD와 같았다. 두 버전의 생산 Go/설정 차이는 snapshot codec 한 파일, 측정 Go probe와 toolchain은 같다. code/script/config source 237/239개 archive와 현재 candidate source를 확인했다.

baseline binary SHA `4a30c3f465563420b75adaf92b3ac282b45a710be8e4ddbfd47500b2c1d96e09`, candidate `c4b8b1d69a5681468692e275097f8bc63e67ec4ae8b6c31f593efd43ef5498bc`다.

Apple M3 Pro·11 CPU·18GiB, macOS 15.8 arm64/APFS·Go 1.27.3/cgo=1, GOMAXPROCS=11/GOGC=100/GOMEMLIMIT=2GiB다. profile은 켜지 않았고, 전체 CI/fuzz와 작은 smoke가 끝난 뒤 본 비교를 별도로 실행했다. CPU 유휴 gate나 OS cache flush는 사용하지 않은 비격리 개발 호스트/warm-cache 관찰이다.

입력은 기존 `var/state-scale-full-20261113`의 동일한 10,000명령 fixture다. active는 주문/활성 10,000/10,000·체결 0개, history는 주문/활성 5,000/0·체결 2,500개다. 각 버전/반복의 runId·초기 자금·WAL/Genesis/lock와 기준 snapshot bytes가 같다. 새 worker/사본을 사용하고 반복별 버전 및 조건 순서를 교대한다.

WAL 복구 뒤 첫 새 Snapshot 생성과 최종 snapshot+전체 WAL 복구를 각각 전후 5회 측정했다. 후자의 tail=0이고 기존 전체 prefix 검사/동일 파일 재확인/sync를 유지한다. 사본/해시/강제 GC/검증·shutdown은 API wall 밖이며 API 내부 자연 GC/I/O 대기는 포함한다. 과거 다른 빌드/시점의 비용 수치를 이번 baseline 대신 쓰지 않는다.

## 실제 결과

각 버전 5회 중앙값 [최소~최대], 40 worker 상태 일치.

| 상태 | 이전 새 생성 ms | 이후 새 생성 ms | 변화 % | 이전 생성 할당 MiB | 이후 생성 할당 MiB | 변화 % |
| --- | --- | --- | ---: | --- | --- | ---: |
| active | 68.564 [66.578~74.782] | 65.176 [58.862~76.921] | -4.94 | 30.376 [30.375~30.378] | 20.461 [20.461~20.463] | -32.64 |
| history | 56.145 [49.562~58.795] | 55.452 [50.264~56.815] | -1.24 | 24.932 [24.929~24.936] | 16.384 [16.384~16.385] | -34.28 |

| 상태 | 복구 | 이전 Open ms | 이후 Open ms | 이전 할당 MiB | 이후 할당 MiB |
| --- | --- | --- | --- | --- | --- |
| active | wal | 77.540 [76.286~103.990] | 77.421 [75.249~81.817] | 52.842 [52.831~52.850] | 52.842 [52.840~52.861] |
| history | wal | 68.448 [67.211~78.506] | 69.243 [67.467~74.436] | 47.762 [47.741~47.795] | 47.747 [47.736~47.760] |
| active | snapshot | 297.363 [293.746~320.356] | 293.552 [291.959~296.149] | 124.160 [124.145~124.174] | 114.248 [114.213~114.269] |
| history | snapshot | 259.361 [255.082~263.870] | 258.019 [255.017~259.770] | 105.832 [105.823~105.849] | 97.283 [97.265~97.322] |

새 snapshot 생성은 WAL 복구 후 첫 publication이다. snapshot 복구의 기존 파일 재확인과 구분한다.
준비/사본/해시/강제 GC/검증은 wall 시간 밖이며 자연 GC·I/O 대기는 포함한다. CPU/할당 profile은 켜지 않는다.
같은 입력·새 worker·warm-cache·비격리 호스트, 느린 표본 포함. HTTP p99/최대 처리량·실제 전원 장애 검증과 구분한다.
실제 OS·전원 장애 검증은 미완료·보류다.

## 해석과 채택

새 생성의 총 할당 중앙값은 active 30.376→20.461MiB(-32.64%), history 24.932→16.384MiB(-34.28%)다. 해당 항목의 반환 bytes 사본을 줄인 코드 경로와 일치한다. 도메인 정수/Reason의 custom MarshalJSON 등 임시 할당은 남아 있어 모든 JSON 할당을 없앴다는 뜻은 아니다. 측정 후 같은 상태의 전체 bytes와 최초 요청/결과가 일치하므로 이력/상태를 생략해 줄인 것이 아니다.

생성 시간 중앙값은 active 68.564→65.176ms(-4.94%), history 56.145→55.452ms(-1.24%)였다. active는 5쌍 중 4쌍이 빨랐고 4번 쌍은 66.578→76.921ms(+15.53%)로 느렸다. history는 3쌍이 빨랐고 1번 49.562→51.369ms(+3.65%), 4번 56.146→56.815ms(+1.19%)가 느렸다. 최소/최대와 이 느린 표본 모두 유지한다. 각각 5회·비격리 호스트의 작은 시간 차이이므로 할당 감소를 안정적인 latency 개선 폭으로 환산하지 않는다.

snapshot Open 할당도 active 124.160→114.248MiB(-7.98%), history 105.832→97.283MiB(-8.08%)로 줄었다. 복구의 canonical 기존 파일 재확인도 같은 writer를 사용하기 때문이다. WAL Open 할당/시간은 거의 같았고 history Open 시간 중앙값은 오히려 68.448→69.243ms로 높았다. 해당 복구 decoder는 변경하지 않았으며 모든 경로가 빨라졌다고 하지 않는다.

개별 stage 중앙값은 다음과 같다. 이 값들을 합해 전체 중앙값을 만들지 않는다.

| 상태 | 단계 | 이전 | 이후 |
| --- | --- | ---: | ---: |
| active | encode/write ms | 45.303 | 43.643 |
| active | file sync ms | 4.494 | 4.106 |
| active | directory sync ms | 3.110 | 1.711 |
| active | 검증 전 누적 RSS high-water MiB | 40.781 | 38.188 |
| history | encode/write ms | 38.864 | 35.185 |
| history | file sync ms | 3.416 | 4.916 |
| history | directory sync ms | 2.840 | 3.195 |
| history | 검증 전 누적 RSS high-water MiB | 36.125 | 32.266 |

RSS는 새 worker 시작 이후의 누적 peak로, Snapshot만의 독립 peak가 아니다. 저장 sync나 커널 I/O의 인과적 개선도 이번 코드/측정으로 확정하지 않는다. **채택 근거는 임시 할당 감소와 bytes/검증·저장·복구 계약 유지**다.

## 검증과 보존

- Go 새 회귀 3개는 독립 json.Marshal 기준과 모든 현재 leaf DTO·정수 경계/nullable 조합·ASCII 256바이트·한글/emoji/U+2028/U+2029/HTML/제어문자·잘못된 UTF-8·16KiB 경계·큰 문자열 뒤 음수 정수의 오류 우선순위, custom/unsupported 입력·Encoder 재사용/오류/short write를 검사했다. 고정 seed의 4,000개 DTO 값도 대조했다.
- 초기 차등 회귀는 NaN을 포함한 오류 객체를 DeepEqual로 비교해 실패했다(`var/snapshot-leaf-targeted-final-20261113.log`). NaN은 자기 자신과도 같지 않으므로 테스트를 오류 type/message·wrapped 도메인 sentinel 검사로 고쳤고 `var/snapshot-leaf-targeted-verified-20261113.log`에서 통과했다. 이 실패 기록도 유지한다.
- Marshal 차등 fuzz 23,409회, snapshot reader fuzz 686,317회(각 30초 설정) 통과. `var/snapshot-leaf-differential-fuzz-20261113.log`, `var/snapshot-leaf-reader-fuzz-20261113.log`에 보존했다. 유한 fuzz를 모든 입력의 증명으로 확장하지 않는다.
- 로컬 전체 CI: portable 229개/23 packages·native race 267개/23 packages(기존 child-helper skip 1개), 각 Python 68개와 진단/저장/프로세스·HTTP/복구 smoke 통과. `var/ci/portable.RoCNKg`, `var/ci/native.Ijh9De`, `var/snapshot-leaf-ci-portable-20261113.log`, `var/snapshot-leaf-ci-native-20261113.log`에 남겼다. 추가 Python 2개는 허용 생산 차이의 WAL/snapshot 분리와 생성 중심 read-only 보고서를 검증한다.
- `var/snapshot-leaf-smoke-20261113`은 두 동결 빌드의 실제 작은 8 worker 비교다. 성능 수치에는 포함하지 않았다.
- 본 자료 `var/snapshot-leaf-full-20261113`: 40 worker/383개 봉인 파일·약 456MiB. 입력 `var/state-scale-full-20261113` 588개 파일도 다시 확인했다. 기존 원본을 덮어쓰거나 삭제하지 않았으며 4GiB 묶음/8GiB 여유 예산을 유지했다(종료 여유 약 11GiB).
- `var/snapshot-leaf-reanalysis-20261113`의 새 JSON/Markdown이 원본과 bytes까지 같다. code archive 237/239개의 member SHA·현재 candidate source·동결 바이너리/입력·개별 대응 쌍은 `var/snapshot-leaf-final-proof-20261113.json`으로 검증했다.

이번 범위는 같은 작은 고정 상태의 warm-cache 생성/복구다. cold-cache·큰 WAL tail·많은 계정/가격 단계·새 HTTP p99/최대 처리량·무제한 장기 운영은 검증하지 않았다. 실제 OS·전원 장애는 **미완료·보류**이고 v1 전체는 계속 Blocked다. 위 구현 완료 시점의 검증은 로컬 결과이며 후속 커밋·병합·푸시와 원격 CI는 따로 확인한다.

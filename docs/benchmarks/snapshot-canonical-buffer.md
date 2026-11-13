# 스냅샷 정규형 비교 버퍼 재사용

2026-11-13, master `884c6cc83a6239db88a39d96a95c851090bf3209`의 배열 최적화 이후 생산 코드를 기준으로 최신 allocation profile을 먼저 확인하고 정규형 비교의 반환 bytes 사본을 줄였다. **할당 감소 목적으로 채택한다.** 같은 active/history 1만 명령과 세 tail에서 버전별 5회·60개 새 worker가 복구/메모리 검증을 통과했고, Open TotalAlloc 중앙값은 6.07~9.37% 감소했다.

Open 시간 변화는 -3.48~+1.21%로 세 조건은 낮고 세 조건은 높았다. GC 뒤 live heap 중앙값 차이는 1.33KiB 이내, 현재 RSS 중앙값은 다섯 조건에서 감소했지만 history tail 0은 41.422→42.688MiB(+1.266MiB) 증가했다. 모든 조건의 복구 시간/RSS 개선으로 해석하지 않는다. 후보의 현재 RSS/누적 Open peak RSS는 모두 45.641MiB 이하로 기존 2GiB 측정 기준을 유지했다.

## 먼저 확인한 최신 할당 비중

이전 [읽기 profile](snapshot-read-profiling.md)은 배열 최적화 전 코드다. 이번에는 후보 수정 전에 같은 두 상태의 최종 snapshot(tail=0)에서 allocation profiler만 상태별 5회·총 10개 새 worker로 수집했다. MemProfileRate=65536, 매 호출 전후 profile 차이를 pprof로 합쳐 읽기 focus에서 본 값이다.

| 상태 | 읽기 추정 할당 MiB/호출 | json.Marshal 누적 MiB/호출·읽기 비중 | bytes.Clone 자체 MiB/호출·읽기 비중 | 배열 증가 자체 MiB/호출·읽기 비중 | 정확한 Open TotalAlloc 중앙값 MiB |
| --- | ---: | --- | --- | --- | ---: |
| active | 32.089 | 14.017 · 43.68% | 9.529 · 29.69% | 8.945 · 27.88% | 105.843 |
| history | 27.795 | 12.313 · 44.30% | 8.475 · 30.49% | 7.500 · 26.98% | 92.189 |

Marshal 누적에는 반환 사본의 Clone이 포함되므로 두 수치를 더하지 않는다. focus도 Open→Read→Decode로 중첩된다. 이는 표본 추정 할당으로 유지 heap/RSS·누수·wall 시간이나 CPU 비중이 아니다. 정규형 재직렬화 경로가 여전히 큰 할당 후보임을 확인하는 근거로 썼고, 아래 전후 비교에는 profiler를 끄고 별도 worker를 사용했다. 전체 함수표·before/after raw profile·합친 protobuf와 모든 scope는 원본/JSON에 보존했다. 최적화 후 allocation profile 비중은 이번에 다시 수집하지 않았다.

## 구현과 유지한 계약

[parseValue](../../internal/snapshot/decode.go)는 UTF-8 검사와 typed Unmarshal 뒤 기존 json.Marshal의 반환 사본을 만드는 대신, decoder별 표준 Encoder/비교 writer를 재사용한다. Encoder가 Write 호출에 빌려 준 출력 bytes를 원문 고정 버퍼와 즉시 비교하고 framing 개행 하나가 정확히 있는지 확인한다. 별도 encoded bytes 사본이나 전체 payload를 보관하지 않으며, Encode 뒤 원문 참조도 해제한다. 표준 라이브러리 내부 encode-state/pool 작업 버퍼·정수 codec 할당과 정규형 재인코딩 자체는 계속 사용한다.

비교 writer는 나눠진 Write/부족한 출력/추가 bytes/잘못된 개행도 거절한다. 원문 16KiB와 비교 위치 16KiB+1 이내로 관리하며 mismatch를 기록하고 Write에는 정상 소비를 반환해 Encoder의 sticky writer error가 다음 비교를 오염시키지 않게 한다. Marshal 오류/bytes 불일치의 외부 오류는 기존 noncanonical JSON value와 같다.

필드 누락/중복/미지/순서/대소문자·기본 HTML/Unicode escaping·nullable/정수·손상 입력·오류 우선순위와 스키마 가정은 유지했다. 고정 leaf 16KiB·깊이64·읽기32KiB·계정/배열 개수·payload2GiB, 두 checksum·변경 reader/EOF 판정·Restore 의미 검사·전체 WAL prefix/최초 응답 대조·fallback·strict sync와 파일 형식/버전도 그대로다. 기존 파일 상한이 전체 상태 메모리를 보장하지 않는 계약은 유지한다. GOMEMLIMIT=2GiB는 Go soft limit이며 측정기는 GC 뒤 heap/현재 RSS와 Open peak RSS에 각각 별도 2GiB 기준을 적용한다.

## 빌드·입력·계측

배열 최적화의 `var/snapshot-array-resident-full-20261113` 봉인 327개 파일과 해당 candidate 생산 source를 확인해 같은 Genesis/runId/전체 WAL/세 snapshot·최초 응답을 복사했다. 총 명령 10,000, snapshot seq 10,000/9,000/5,000과 이후 명령 0/1,000/5,000이다. active 최종 주문/활성/체결은 9,995/9,992/1, history는 4,999/0/2,499이고 최초 결과는 각각 10,000개다. 이번 baseline과 candidate는 모두 배열 최적화를 포함한다. 과거 다른 빌드의 비용을 baseline으로 섞지 않았다.

baseline/candidate source archive는 각각 261/262개 파일이며 실제 source 멤버와 내용/바이너리 SHA를 검증했다. 생산 차이는 decode.go 하나, Go 측정 probe는 같다. 후보 archive의 추가 파일은 회귀 테스트다. baseline 이후 새 profile scratch 소유권 검사·회귀를 보강한 Python collector 차이도 source에 기록되며 공개 Open 계측/입력·Go probe는 바뀌지 않았다. 최초 smoke 후보는 별도 이름으로 보존했고 최종 smoke/full candidate를 다시 동결했다. 두 빌드는 uncommitted 실험/테스트 소스를 포함하므로 revision만으로 재현했다고 주장하지 않는다.

| 버전 | 바이너리 SHA-256 |
| --- | --- |
| baseline | `124309e3815cf182c131058bb5a8f655b5b2b0a70f46d59608790c51fba467c8` |
| candidate | `92302dc9abcd619a27d6c9b5a96e21e18bfd41f4e197eadbf3e61e836bf481e1` |

Apple M3 Pro·11 CPU·18GiB·macOS 15.8 arm64/APFS·Go 1.27.3/cgo=1, GOMAXPROCS=11/GOGC=100/GOMEMLIMIT=2GiB다. 준비·사본·해시·profile·강제 GC·검증·종료는 비교 Open wall 밖이며 자연 GC와 Open 내부 I/O 대기는 포함한다. CPU/IO observer는 켜지 않았다. 조건 순서를 회전/반전하고 각 쌍의 버전 순서를 교대했다. warm-cache·비격리 호스트이며 유휴 gate/cache flush는 없다. 느린 표본은 전부 포함했다.

heap은 엔진을 살려 둔 GC 뒤 HeapAlloc, 현재 RSS는 바로 이어 읽은 Mach resident_size다. 누적 peak RSS는 worker 시작부터 Open 직후까지 getrusage high-water이며 샘플 시점이 다르므로 현재 RSS와 대소 관계를 강제하지 않는다. physical footprint/compressed memory/swap은 측정하지 않았다.

## 전후 결과


각 버전 5회, 60 worker. 중앙값 [최소~최대].

| 상태 | 이후 명령 | 이전 Open ms | 이후 Open ms | 변화 % | 이전 TotalAlloc MiB | 이후 TotalAlloc MiB | 변화 % |
| --- | ---: | --- | --- | ---: | --- | --- | ---: |
| active | 0 | 291.666 [290.602~343.641] | 291.974 [291.175~304.978] | 0.11 | 105.827 [105.811~105.840] | 95.909 [95.884~95.914] | -9.37 |
| active | 1000 | 269.261 [264.391~273.132] | 269.655 [266.894~273.033] | 0.15 | 101.343 [101.329~101.369] | 92.434 [92.403~92.447] | -8.79 |
| active | 5000 | 187.059 [183.032~189.966] | 184.703 [181.262~189.496] | -1.26 | 79.447 [79.444~79.459] | 74.477 [74.454~74.493] | -6.26 |
| history | 0 | 252.388 [250.976~257.254] | 255.438 [251.457~261.977] | 1.21 | 92.174 [92.151~92.196] | 83.616 [83.603~83.630] | -9.28 |
| history | 1000 | 238.683 [230.735~261.954] | 235.270 [229.267~267.245] | -1.43 | 88.921 [88.914~88.931] | 81.235 [81.190~81.239] | -8.64 |
| history | 5000 | 164.659 [159.820~169.037] | 158.925 [157.751~177.811] | -3.48 | 70.474 [70.471~70.482] | 66.195 [66.183~66.231] | -6.07 |

| 상태 | 이후 명령 | 이전 GC 뒤 heap MiB | 이후 GC 뒤 heap MiB | 이전 GC 뒤 RSS MiB | 이후 GC 뒤 RSS MiB | 이전 peak RSS MiB | 이후 peak RSS MiB |
| --- | ---: | --- | --- | --- | --- | --- | --- |
| active | 0 | 10.912 [10.911~10.913] | 10.912 [10.912~10.917] | 48.109 [44.578~48.922] | 45.234 [44.734~45.609] | 48.047 [44.578~48.875] | 45.188 [44.734~45.562] |
| active | 1000 | 10.859 [10.858~10.859] | 10.859 [10.859~10.877] | 44.938 [44.516~45.828] | 43.172 [40.641~43.938] | 44.922 [44.516~45.828] | 43.172 [40.641~43.781] |
| active | 5000 | 10.657 [10.657~10.658] | 10.657 [10.657~10.670] | 35.797 [33.969~36.297] | 34.688 [33.516~35.734] | 35.797 [33.969~36.266] | 34.656 [33.516~35.547] |
| history | 0 | 9.623 [9.622~9.628] | 9.623 [9.622~9.649] | 41.422 [40.359~41.797] | 42.688 [41.516~45.641] | 41.422 [40.359~41.766] | 42.688 [41.516~45.641] |
| history | 1000 | 9.645 [9.643~9.646] | 9.644 [9.643~9.646] | 43.422 [42.812~43.766] | 43.359 [38.953~44.516] | 43.422 [42.781~43.750] | 43.359 [38.906~44.516] |
| history | 5000 | 9.504 [9.502~9.536] | 9.503 [9.501~9.529] | 33.875 [30.766~34.312] | 32.953 [32.438~34.391] | 33.594 [30.750~34.250] | 32.766 [32.344~34.312] |

준비/해시/강제 GC/후속 검증은 Open wall 밖이다. 자연 GC·내부 I/O 대기는 포함한다.
heap은 엔진을 살려 둔 강제 GC 뒤 HeapAlloc, GC 뒤 RSS는 Mach resident_size, peak RSS는 worker 시작부터 Open 직후까지 high-water다. 2GiB budget 유지.
동일 입력·32KiB decoder·canonical/이중 checksum·WAL prefix/최초 응답/전체 상태·strict sync 유지. 느린 표본도 포함.
warm-cache·비격리 호스트이며 HTTP p99/최대 처리량 개선을 증명하지 않는다. 실제 OS·전원 장애 검증은 미완료·보류다.

Open이 빨랐던 대응 쌍은 active tail 0/1천/5천에서 2/3/3개, history는 1/3/3개였다. history tail 1천 후보 최대 267.245ms, tail 5천 후보 최대 177.811ms 등 느린 표본도 남겼다. 읽기·restore/replay/confirm의 최소/중앙/최대와 모든 쌍의 변화율은 [JSON](20261113-snapshot-canonical-buffer.json)에 포함했다. CPU 비용 감소나 HTTP p99·최대 지속 처리량 향상은 이번 결과로 확정하지 않는다.

## 검증과 보존

60회 모두 정확한 선택 snapshot/재생 수·fallback 부재를 확인했다. 직접 최초 응답 480개·동일 ID 재시도 120개·후속 체결/취소·독립 잔고/예약금·주문/체결 cursor 기대값과 전체 canonical 최종 snapshot/전체 WAL 불변이 일치했다. 최종 snapshot bytes에는 최초 결과 1만 개도 포함된다. profile 10회도 전체 상태 bytes를 확인했다. 기존 engine/checkpoint 복구 oracle과 독립 정수 기대값을 사용하며 독립 매칭 엔진과의 비교는 아니다.

표준 Marshal와 기존 strict/canonical 판정을 기준으로 모든 leaf DTO·중첩 필드 변이·Marshal 오류/재사용·HTML/Unicode/invalid UTF-8·16KiB 경계/escaping 확대·Encoder framing/분할 Write/추가/부족 bytes를 검사했다. 기존 golden/손상/IO 오류·변경 reader·실제 프로세스/HTTP/저장 복구 회귀도 유지했다.

- 로컬 native race CI: `var/ci/native.PqACLQ`, 23 packages/277 tests, 명시적 자식 helper skip 1·Python 94개·진단 native smoke 통과.
- 로컬 portable CI: `var/ci/portable.ibHlM9`, 23 packages/238 tests, Python 94개·진단 portable smoke 통과.
- 새 buffer 재사용 fuzz 30초 10,647회, 기존 canonical leaf/strict 차등 fuzz 30초 365,801회, SnapshotReader fuzz 30초 12,293회 통과. seed/기존 cache corpus와 새 interesting input을 포함하며 실행 수를 성능 개선 수치로 사용하지 않는다.

최신 profile 요약·비계측 JSON/Markdown을 새 폴더에서 다시 생성해 bytes가 같았다. 주 묶음 `var/canonical-buffer-full-20261113`은 435개 파일/58.60MiB이며 원 profile·prepared hash receipt·두 source archive/고정 빌드·공통 fixture·모든 결과/로그를 보존한다. 성공 소유 scratch만 소유권/파일 집합/hash/size를 확인해 정리했고 원본/고정 빌드/실패는 삭제하지 않았다. 원래 입력 경로 접근이나 probe 바이너리 실행 없이 재집계했다. [자료 검증 JSON](20261113-snapshot-canonical-buffer-artifacts.json)과 [명령·제한](../snapshot-canonical-buffer.md)을 따른다.

모든 성능 비교가 끝난 뒤 전체 CI 준비 공간을 위해 프로젝트 Go 컴파일 캐시만 정리했다. 실험 원본/고정 빌드/profile은 유지했으며, 측정 중 캐시 정리나 호스트 상태 변경을 수행한 것은 아니다. 공간 기준도 낮추지 않았다.

실제 OS·전원 장애 검증은 **미완료·보류**, v1 전체 계획은 Blocked다. 이 기록은 구현 시점의 로컬 근거이며 커밋·merge·push·해당 커밋의 원격 CI는 후속 발행에서 별도 확인한다.

병합 전 검토에서 생산 코드 추가 수정 없이 snapshot/statescale race·Python 94개, 동결 후보 source 일치·원본 435개/입력 327개 파일 검증과 새 폴더의 profile/JSON/Markdown 재집계 bytes 일치를 다시 확인했다. 위 측정은 구현 시점의 근거이며 후속 발행 커밋과 원격 CI는 별도로 확인한다.

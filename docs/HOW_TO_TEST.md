# How to Test

This guide outlines the testing strategy for `yutu`. We combine high-level strategic goals with specific patterns to ensure code reliability and maintainability.

## 0. High-Level Strategy

### 0.1. Philosophy
Our testing philosophy focuses on **confidence** and **maintainability**. Tests should give us confidence that the code works as expected and allow us to refactor safely. We prefer comprehensive unit tests that mock external dependencies (YouTube API) over flaky end-to-end tests.

### 0.2. Test-Driven Development (TDD)
We encourage a TDD approach, especially when adding new resources:
1.  **Define the Interface**: Write the `TestNew<Resource>` test first. This forces you to design the API surface (Options pattern) before implementation.
2.  **Define Behavior**: Write the test for a specific method (e.g., `Get` or `Insert`) using the shared `common.NewTestService` helper to mock the expected YouTube API interaction.
3.  **Implement**: Write the code to make the tests pass.
4.  **Refactor**: Clean up the code while keeping tests green.

### 0.3. Coverage Targets
- **Target**: We aim for **>80%** code coverage for domain logic in `pkg/`.
- **Critical Paths**: Authentication, flag parsing, and API request construction must have near 100% coverage.
- **Tools**: Use `go test -cover`, `go test -coverprofile`, `go tool cover -func`, or the Bazel coverage report to identify gaps.

### 0.4. Continuous Integration
Tests are run automatically on every Pull Request via GitHub Actions.
- **Fast Feedback**: Unit tests (`go test ./...`) should be fast.
- **Hermeticity**: Tests should not depend on external internet access. Use the shared `pkg/common` HTTP test helpers for YouTube API calls, and local `httptest` servers for non-YouTube HTTP endpoints such as OAuth token exchanges.

---

## 1. Test Location & Naming

- **Location**: Tests must be co-located with the code in `pkg/<resource>/<resource>_test.go`.
- **Naming**: Use `Test<Resource>_<Method>` (e.g., `TestChannel_Get`, `TestPlaylist_Insert`).
- **Constructor**: `TestNew<Resource>` tests the option pattern implementation.

## 2. Testing Constructors (`New<Resource>`)

We use the Functional Options pattern. Tests must ensure that all options are applied correctly and edge cases are handled.

**Required Test Cases:**
- `with all options`: Verify every field is set correctly.
- `with no options`: Verify defaults.
- `with nil/false boolean options`: Ensure nil pointers and false values are handled distinctively.
- `with zero/negative max results`: Verify boundary logic (e.g., `0` -> `math.MaxInt64`, `<0` -> `1`).
- `with empty string values`: Ensure empty strings don't crash or set incorrect defaults if not intended.

**Example Structure:**
```go
func TestNewChannel(t *testing.T) {
    type args struct {
        opts []Option
    }
    tests := []struct {
        name string
        args args
        want IChannel[youtube.Channel]
    }{
        {
            name: "with all options",
            args: args{opts: []Option{WithTitle("Test"), ...}},
            want: &Channel{Title: "Test", ...},
        },
        // ... other cases
    }
    // Run loop using reflect.DeepEqual
}
```

## 3. Testing Methods (`Get`, `List`, `Insert`, `Update`, `Delete`)

We **do not** use a mocking library for the YouTube service. Instead, resource package tests should use `common.NewTestService`, which wraps `httptest.NewServer` and returns a real `*youtube.Service` pointed at the local test server. This allows us to verify exact HTTP requests (method, query params, body) while keeping tests hermetic.

### 3.1. Mocking the API

Create a handler that acts as the YouTube API, then pass it to `common.NewTestService`.

```go
svc := common.NewTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    // 1. Verify Request
    if r.Method != "GET" {
        t.Errorf("expected GET, got %s", r.Method)
    }
    if r.URL.Query().Get("part") != "snippet" {
        t.Errorf("expected part=snippet")
    }

    // 2. Mock Response
    w.Header().Set("Content-Type", "application/json")
    _, _ = w.Write([]byte(`{ "items": [ { "id": "1", "snippet": { "title": "Test" } } ] }`))
}))
```

### 3.2. Injecting the Mock Service

Inject the mock service through the resource's `WithService` option.

```go
resource := NewVideo(
    WithService(svc),
    WithIds([]string{"video-id"}),
    WithMaxResults(1),
)

got, err := resource.Get()
```

Prefer `common.NewTestService` over directly calling `httptest.NewServer` for YouTube API tests. Direct `httptest.NewServer` is still appropriate for other HTTP services, such as OAuth token endpoint tests in `pkg/auth`.

### 3.3. Pagination

For `Get` methods that support pagination, prefer `common.PaginationHandler` when the default mock item shape is enough.

```go
svc := common.NewTestService(t, common.PaginationHandler("video"))
resource := NewVideo(WithService(svc), WithMaxResults(22))

got, err := resource.Get()
```

When the resource needs a custom item shape, pass a formatter function:

```go
svc := common.NewTestService(
    t,
    common.PaginationHandler("comment", func(prefix string, i int) string {
        return fmt.Sprintf(
            `{"id":"%s-%d","snippet":{"textDisplay":"Comment %d"}}`,
            prefix, i, i,
        )
    }),
)
```

If the pagination behavior itself is under test, write an explicit handler that checks `pageToken`.

```go
func(w http.ResponseWriter, r *http.Request) {
    token := r.URL.Query().Get("pageToken")
    if token == "" {
        // Return page 1 and nextPageToken
    } else if token == "page-2" {
        // Return page 2
    }
}
```

### 3.4. Partial Results and Output Errors

For paginated list methods, `common.Paginate` may return both partial items and an error when a later page fails. `List` methods should still print the partial items and return all errors:

```go
items, err := r.Get()
if err != nil && items == nil {
    return err
}

return errors.Join(
    err,
    common.PrintList(...),
)
```

Add focused regression coverage when changing this behavior, especially for cases where both the fetch and writer fail.

### 3.5. Batch Operations

For methods that operate on multiple IDs, do not stop after the first per-item failure unless later items depend on that failure. Accumulate errors and continue:

```go
func (r *Resource) Delete(writer io.Writer) (errs error) {
    if errs = r.EnsureService(); errs != nil {
        return errs
    }

    for _, id := range r.Ids {
        err := call.Do()
        if err != nil {
            errs = errors.Join(errs, errDeleteResource, err)
            continue
        }

        _, err = fmt.Fprintf(writer, "Resource %s deleted\n", id)
        errs = errors.Join(errs, err)
    }

    return errs
}
```

Keep checks around operation errors when they control flow. For example, if an API call fails, do not print a success message for that item.

## 4. Testing Output Formats

For `List` methods, verify that all supported output formats (JSON, YAML, Table) work without error and write to the buffer.

```go
tests := []struct {
    name   string
    output string // "json", "yaml", "table"
}{
    { "list json", "json" },
    { "list yaml", "yaml" },
    { "list table", "table" },
}
```

Most resource packages can use `common.RunListTest` for the standard JSON/YAML/table matrix:

```go
func TestVideo_List(t *testing.T) {
    common.RunListTest(
        t,
        `{"items":[{"id":"video-id","snippet":{"title":"Title"}}]}`,
        func(svc *youtube.Service, output string) func(io.Writer) error {
            v := NewVideo(WithService(svc), WithOutput(output), WithMaxResults(1))
            return v.List
        },
    )
}
```

When testing writer failures, use a small local writer stub and assert the returned error:

```go
type errWriter struct{}

func (errWriter) Write([]byte) (int, error) {
    return 0, errors.New("write failed")
}
```

## 5. Testing Timeouts and Cancellation

For timeout or cancellation code, prefer `testing/synctest` when possible. It keeps tests fast and deterministic without real sleeps.

```go
func TestWaitForCode_Timeout(t *testing.T) {
    synctest.Test(t, func(t *testing.T) {
        s := NewY2BService(WithCallbackTimeout(2 * time.Minute)).(*svc)

        _, err := s.waitForCode(make(chan string))
        if err == nil || !strings.Contains(err.Error(), "authorization timed out after 2m0s") {
            t.Fatalf("waitForCode() error = %v", err)
        }
    })
}
```

Use normal contexts for immediate cancellation cases:

```go
ctx, cancel := context.WithCancel(t.Context())
cancel()

s := NewY2BService(WithContext(ctx)).(*svc)
_, err := s.waitForCode(make(chan string))
```

## 6. Running Tests

You can run tests using standard Go tools or Bazel.

**Standard Go:**
```bash
go test ./pkg/...
# Verbose single test
go test -v ./pkg/channel -run TestChannel_Get
```

**Bazel:**
```bash
bazel test //pkg/...
```

//! The conformance runner: every vector in `../conformance/cases`, driven
//! against a fresh wiremock gateway.
//!
//! `cargo test` cannot register tests at runtime, so this target sets
//! `harness = false` and hands the cases to `libtest-mimic`.

use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::pin::pin;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use libtest_mimic::{Arguments, Failed, Trial};
use santati::{
    AuditEvent, DefinitionInput, DefinitionUpdate, EmitResult, Error, EventInput, Events,
    ListParams, PageParams, Santati, StreamExt,
};
use serde_json::{json, Map, Value};
use wiremock::matchers::any;
use wiremock::{Mock, MockServer, Request, Respond, ResponseTemplate};

fn main() {
    let args = Arguments::from_args();
    let cases = load_cases();
    if cases.is_empty() {
        eprintln!("no conformance cases loaded from ../conformance/cases");
        std::process::exit(1);
    }
    let trials: Vec<Trial> = cases
        .into_iter()
        .map(|case| {
            let id = case["id"]
                .as_str()
                .expect("every conformance case has an id")
                .to_string();
            Trial::test(id, move || run_case(&case))
        })
        .collect();
    libtest_mimic::run(&args, trials).exit();
}

/// Every `conformance/cases/*.json`, in file order.
fn load_cases() -> Vec<Value> {
    let directory = Path::new(env!("CARGO_MANIFEST_DIR")).join("../conformance/cases");
    let mut files: Vec<PathBuf> = std::fs::read_dir(&directory)
        .unwrap_or_else(|error| panic!("cannot read {}: {error}", directory.display()))
        .map(|entry| entry.expect("cannot read a cases directory entry").path())
        .filter(|path| {
            path.extension()
                .is_some_and(|extension| extension == "json")
        })
        .collect();
    files.sort();

    let mut cases = Vec::new();
    for file in files {
        let text = std::fs::read_to_string(&file)
            .unwrap_or_else(|error| panic!("cannot read {}: {error}", file.display()));
        let document: Value = serde_json::from_str(&text)
            .unwrap_or_else(|error| panic!("cannot parse {}: {error}", file.display()));
        let list = document["cases"]
            .as_array()
            .unwrap_or_else(|| panic!("{} has no cases array", file.display()));
        cases.extend(list.iter().cloned());
    }
    cases
}

/// One case: a fresh gateway, a fresh runtime, and the case's own client.
fn run_case(case: &Value) -> Result<(), Failed> {
    let runtime = tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .map_err(|error| Failed::from(format!("cannot build a Tokio runtime: {error}")))?;
    let gateway = runtime.block_on(start_gateway(case))?;
    let outcome = runtime.block_on(drive(case, &gateway));
    drop(gateway);
    drop(runtime);
    outcome
}

struct Gateway {
    base_url: String,
    server: Option<MockServer>,
}

impl Gateway {
    async fn recorded(&self) -> Vec<Value> {
        match &self.server {
            Some(server) => server
                .received_requests()
                .await
                .unwrap_or_default()
                .iter()
                .map(recorded_request)
                .collect(),
            None => Vec::new(),
        }
    }
}

/// `{"unreachable": true}` gets no server at all, so nothing is recorded.
async fn start_gateway(case: &Value) -> Result<Gateway, Failed> {
    let input = &case["input"];
    let gateway = input
        .get("gateway")
        .ok_or_else(|| Failed::from(format!("{}: no input.gateway", case_id(case))))?;
    if gateway.get("unreachable").and_then(Value::as_bool) == Some(true) {
        return Ok(Gateway {
            base_url: "http://127.0.0.1:1".to_string(),
            server: None,
        });
    }

    let responses: Vec<Value> = match gateway.get("sequence").and_then(Value::as_array) {
        Some(sequence) => sequence.clone(),
        None => vec![gateway.clone()],
    };
    if responses.is_empty() {
        return Err(Failed::from(format!(
            "{}: the gateway sequence is empty",
            case_id(case)
        )));
    }

    let server = MockServer::builder().start().await;
    let responder = GatewayResponder {
        responses: Arc::new(responses),
        seen: Arc::new(AtomicUsize::new(0)),
    };
    Mock::given(any())
        .respond_with(responder)
        .mount(&server)
        .await;

    let base_path = input["client"]
        .get("base_path")
        .and_then(Value::as_str)
        .unwrap_or_default();
    Ok(Gateway {
        base_url: format!("{}{base_path}", server.uri()),
        server: Some(server),
    })
}

/// Request *i* gets `sequence[min(i, len - 1)]`.
struct GatewayResponder {
    responses: Arc<Vec<Value>>,
    seen: Arc<AtomicUsize>,
}

impl Respond for GatewayResponder {
    fn respond(&self, _request: &Request) -> ResponseTemplate {
        let seen = self.seen.fetch_add(1, Ordering::SeqCst);
        template(&self.responses[seen.min(self.responses.len() - 1)])
    }
}

fn template(response: &Value) -> ResponseTemplate {
    let status = response
        .get("status")
        .and_then(Value::as_u64)
        .unwrap_or(200) as u16;
    let mut template = ResponseTemplate::new(status);
    if let Some(headers) = response.get("headers").and_then(Value::as_object) {
        for (name, value) in headers {
            if let Some(value) = value.as_str() {
                template = template.insert_header(name.as_str(), value.to_string());
            }
        }
    }
    if let Some(body) = response.get("body") {
        if let Some(json) = body.get("json") {
            template = template.set_body_json(json.clone());
        } else if let Some(text) = body.get("text").and_then(Value::as_str) {
            template = template.set_body_raw(text.as_bytes().to_vec(), "text/plain; charset=utf-8");
        }
    }
    if let Some(delay) = response.get("delay_ms").and_then(Value::as_u64) {
        template = template.set_delay(Duration::from_millis(delay));
    }
    template
}

/// Build the case's client and run the case's operation.
async fn drive(case: &Value, gateway: &Gateway) -> Result<(), Failed> {
    let input = &case["input"];
    let config = input
        .get("client")
        .ok_or_else(|| Failed::from(format!("{}: no input.client", case_id(case))))?;

    let mut builder = Santati::builder(
        config
            .get("api_key")
            .and_then(Value::as_str)
            .unwrap_or_default(),
    )
    .base_url(gateway.base_url.clone());
    if let Some(trail) = config.get("trail").and_then(Value::as_str) {
        builder = builder.trail(trail);
    }
    if let Some(timeout_ms) = config.get("timeout_ms").and_then(Value::as_u64) {
        builder = builder.timeout(Duration::from_millis(timeout_ms));
    }
    if let Some(max_retries) = config.get("max_retries").and_then(Value::as_u64) {
        builder = builder.max_retries(max_retries as u32);
    }
    if let (Some(initial), Some(max)) = (
        config.get("initial_backoff_ms").and_then(Value::as_u64),
        config.get("max_backoff_ms").and_then(Value::as_u64),
    ) {
        builder = builder.backoff(Duration::from_millis(initial), Duration::from_millis(max));
    }
    if let Some(headers) = config.get("headers").and_then(Value::as_object) {
        for (name, value) in headers {
            builder = builder.header(name.as_str(), value.as_str().unwrap_or_default());
        }
    }

    if let Some(batch_size) = config.get("batch_size").and_then(Value::as_u64) {
        builder = builder.batch_size(batch_size as usize);
    }
    if let Some(interval) = config.get("flush_interval_ms").and_then(Value::as_u64) {
        builder = builder.flush_interval(Duration::from_millis(interval));
    }
    let outcomes: Arc<Mutex<Vec<Value>>> = Arc::new(Mutex::new(Vec::new()));
    if case["operation"] == "emit_outbox" {
        let max_pending = config
            .get("max_pending")
            .and_then(Value::as_u64)
            .unwrap_or(10_000) as usize;
        match santati::MemoryOutbox::new(max_pending) {
            Ok(store) => builder = builder.outbox(store),
            Err(error) => return compare_outcome(case, gateway, Err(&error)).await,
        }
        builder = register_hooks(builder, input.get("hooks"), outcomes.clone());
    }

    let built = builder.build();
    let outcome: Result<Value, Error> = match &built {
        Err(error) => Err(error.clone()),
        Ok(client) => {
            let events = client.events();
            match case["operation"].as_str().unwrap_or_default() {
                "emit" => events
                    .emit(event_input(&input["event"]))
                    .await
                    .map(emit_json),
                "emit_batch" => {
                    let batch = input
                        .get("events")
                        .and_then(Value::as_array)
                        .map(|events| events.iter().map(event_input).collect())
                        .unwrap_or_default();
                    events.emit_batch(batch).await.map(batch_json)
                }
                "emit_outbox" => {
                    let (results, failure) = run_emit_outbox(client, input).await;
                    match failure {
                        Some(error) => Err(error),
                        None => Ok(json!({
                            "results": results,
                            "outcomes": outcomes.lock().unwrap().clone(),
                        })),
                    }
                }
                "list" => events
                    .list(list_params(&input["params"]))
                    .await
                    .map(|page| {
                        json!({
                            "results": page.results,
                            "next_cursor": page.next_cursor,
                        })
                    }),
                "iterate" => collect(events, list_params(&input["params"]))
                    .await
                    .map(|events| json!(events)),
                operation => match schema_operation(client, operation, input).await {
                    Some(outcome) => outcome,
                    None => {
                        return Err(Failed::from(format!(
                            "{}: unknown operation {operation}",
                            case_id(case)
                        )))
                    }
                },
            }
        }
    };

    compare_outcome(case, gateway, outcome.as_ref()).await
}

fn emit_json(result: EmitResult) -> Value {
    json!({
        "event": result.event,
        "duplicate": result.duplicate,
        "idempotency_key": result.idempotency_key,
        "queued": result.queued,
    })
}

/// Emit every event through the outbox, stopping at the first SDK error, then
/// close the client.
async fn run_emit_outbox(client: &Santati, input: &Value) -> (Vec<Value>, Option<Error>) {
    let mut results = Vec::new();
    let mut failure = None;
    for event in input
        .get("events")
        .and_then(Value::as_array)
        .map(Vec::as_slice)
        .unwrap_or_default()
    {
        match client.events().emit(event_input(event)).await {
            Ok(result) => results.push(emit_json(result)),
            Err(error) => {
                failure = Some(error);
                break;
            }
        }
    }
    let closed = client.close().await;
    if failure.is_none() {
        failure = closed.err();
    }
    (results, failure)
}

/// The runner's hooks per `conformance/README.md`; `post_send` is always
/// registered and records one outcome per call.
fn register_hooks(
    mut builder: santati::Builder,
    hooks: Option<&Value>,
    outcomes: Arc<Mutex<Vec<Value>>>,
) -> santati::Builder {
    if let Some(pre) = hooks.and_then(|hooks| hooks.get("pre_send")) {
        let raise = pre.get("raise").and_then(Value::as_bool) == Some(true);
        let drop_events: Vec<String> = pre
            .get("drop_events")
            .and_then(Value::as_array)
            .map(|names| {
                names
                    .iter()
                    .filter_map(|name| name.as_str().map(str::to_string))
                    .collect()
            })
            .unwrap_or_default();
        let set_metadata = pre.get("set_metadata").and_then(Value::as_object).cloned();
        builder = builder.pre_send(move |mut event| {
            if raise {
                panic!("conformance pre_send");
            }
            if drop_events.contains(&event.event) {
                return None;
            }
            if let Some(extra) = &set_metadata {
                let metadata = event.metadata.get_or_insert_with(HashMap::new);
                for (key, value) in extra {
                    metadata.insert(key.clone(), value.as_str().unwrap_or_default().to_string());
                }
            }
            Some(event)
        });
    }
    let raise = hooks
        .and_then(|hooks| hooks.get("post_send"))
        .and_then(|post| post.get("raise"))
        .and_then(Value::as_bool)
        == Some(true);
    builder.post_send(move |event, outcome| {
        let mut record = json!({
            "event": serde_json::to_value(event).unwrap_or(Value::Null),
            "status": outcome.status.as_str(),
            "id": outcome.id.clone(),
        });
        if let Some(error) = &outcome.error {
            record["error"] = error_json(error);
        }
        outcomes.lock().unwrap().push(record);
        if raise {
            panic!("conformance post_send");
        }
    })
}

/// The stream's items, and the error a later page raised.
async fn collect(events: Events<'_>, params: ListParams) -> Result<Vec<AuditEvent>, Error> {
    let mut stream = pin!(events.iterate(params));
    let mut collected = Vec::new();
    while let Some(event) = stream.next().await {
        collected.push(event?);
    }
    Ok(collected)
}

fn batch_json(batch: santati::BatchResult) -> Value {
    json!({
        "accepted": batch.accepted,
        "rejected": batch.rejected,
        "results": batch
            .results
            .iter()
            .map(|item| json!({
                "index": item.index,
                "status": item.status.as_str(),
                "id": item.id.clone(),
                "error": item.error.as_ref().map(|error| json!({
                    "code": error.code.clone(),
                    "message": error.message.clone(),
                    "field": error.field.clone(),
                })),
            }))
            .collect::<Vec<Value>>(),
    })
}

async fn compare_outcome(
    case: &Value,
    gateway: &Gateway,
    outcome: Result<&Value, &Error>,
) -> Result<(), Failed> {
    let id = case_id(case);
    let expectation = &case["expect"];
    let mut binder = Binder::default();
    match outcome {
        Ok(actual) => {
            let expected = expectation
                .get("ok")
                .ok_or_else(|| Failed::from(format!("{id}: expected an error, got {actual}")))?;
            compare(expected, actual, &mut binder, true, true)?;
        }
        Err(error) => {
            let expected = expectation
                .get("error")
                .ok_or_else(|| Failed::from(format!("{id}: unexpected error: {error}")))?;
            // An error vector only pins the keys it names; `message` is never
            // one of them.
            compare(expected, &error_json(error), &mut binder, true, false)?;
        }
    }
    check_requests(&id, expectation, gateway, &mut binder).await
}

fn error_json(error: &Error) -> Value {
    let details = error.details();
    json!({
        "kind": error.kind().as_str(),
        "status": details.status,
        "code": details.code.clone(),
        "field": details.field.clone(),
        "retry_after": details.retry_after,
    })
}

async fn check_requests(
    case_id: &str,
    expectation: &Value,
    gateway: &Gateway,
    binder: &mut Binder,
) -> Result<(), Failed> {
    let Some(expected) = expectation.get("requests").and_then(Value::as_array) else {
        return Ok(());
    };
    let actual = gateway.recorded().await;
    if actual.len() != expected.len() {
        return Err(Failed::from(format!(
            "{case_id}: expected {} request(s), recorded {}: {actual:?}",
            expected.len(),
            actual.len()
        )));
    }
    for (index, request) in expected.iter().enumerate() {
        assert_request(case_id, index, request, &actual[index], binder)?;
    }
    Ok(())
}

/// A request matches on method, path, a header subset and — when the vector
/// says so — the exact JSON body.
fn assert_request(
    case_id: &str,
    index: usize,
    expected: &Value,
    actual: &Value,
    binder: &mut Binder,
) -> Result<(), Failed> {
    let prefix = format!("{case_id}: request {index}");
    let expected_method = expected.get("method").and_then(Value::as_str);
    let actual_method = actual.get("method").and_then(Value::as_str);
    if expected_method != actual_method {
        return Err(Failed::from(format!(
            "{prefix}: expected {expected_method:?}, sent {actual_method:?}"
        )));
    }
    let expected_path = expected.get("path").and_then(Value::as_str);
    let actual_path = actual.get("path").and_then(Value::as_str);
    if expected_path != actual_path {
        return Err(Failed::from(format!(
            "{prefix}: expected path {expected_path:?}, sent {actual_path:?}"
        )));
    }
    if let Some(expected_headers) = expected.get("headers").and_then(Value::as_object) {
        let actual_headers = actual.get("headers").and_then(Value::as_object);
        for (name, expected_value) in expected_headers {
            let actual_value = actual_headers.and_then(|headers| headers.get(&name.to_lowercase()));
            if actual_value != Some(expected_value) {
                return Err(Failed::from(format!(
                    "{prefix}: header {name} expected {expected_value}, sent {actual_value:?}"
                )));
            }
        }
    }
    if let Some(expected_body) = expected.get("body") {
        let actual_body = actual.get("body").unwrap_or(&Value::Null);
        compare(expected_body, actual_body, binder, false, true)?;
    }
    Ok(())
}

fn recorded_request(request: &Request) -> Value {
    let path = match request.url.query() {
        Some(query) if !query.is_empty() => format!("{}?{query}", request.url.path()),
        _ => request.url.path().to_string(),
    };
    let mut headers = Map::new();
    for (name, value) in request.headers.iter() {
        headers.insert(
            name.as_str().to_string(),
            Value::String(value.to_str().unwrap_or_default().to_string()),
        );
    }
    let body = if request.body.is_empty() {
        Value::Null
    } else {
        serde_json::from_slice(&request.body).unwrap_or(Value::Null)
    };
    json!({
        "method": request.method.as_str(),
        "path": path,
        "headers": Value::Object(headers),
        "body": body,
    })
}

/// `{"$generated": "label"}` bindings, shared by every comparison in one case.
#[derive(Default)]
struct Binder {
    bound: HashMap<String, String>,
}

/// Deep-compare `expected` with `actual` after removing null members from
/// both sides. `strict_members` additionally requires the two objects to hold
/// the same keys.
fn compare(
    expected: &Value,
    actual: &Value,
    binder: &mut Binder,
    strip_nulls: bool,
    strict_members: bool,
) -> Result<(), Failed> {
    let (expected, actual) = if strip_nulls {
        (strip_null_members(expected), strip_null_members(actual))
    } else {
        (expected.clone(), actual.clone())
    };
    compare_values(&expected, &actual, binder, "$", strict_members)
}

fn compare_values(
    expected: &Value,
    actual: &Value,
    binder: &mut Binder,
    path: &str,
    strict_members: bool,
) -> Result<(), Failed> {
    if let Some(label) = generated_label(expected) {
        let value = actual
            .as_str()
            .filter(|value| !value.is_empty())
            .ok_or_else(|| mismatch(path, expected, actual))?;
        match binder.bound.get(&label) {
            Some(bound) if bound == value => return Ok(()),
            Some(bound) => {
                return Err(Failed::from(format!(
                    "{path}: $generated {label:?} was {bound:?}, now {value:?}"
                )))
            }
            None => {
                if let Some((other, _)) = binder
                    .bound
                    .iter()
                    .find(|(other, bound)| other.as_str() != label && bound.as_str() == value)
                {
                    return Err(Failed::from(format!(
                        "{path}: {value:?} is bound to {other:?}, but {label:?} must differ"
                    )));
                }
                binder.bound.insert(label, value.to_string());
                return Ok(());
            }
        }
    }

    match (expected, actual) {
        (Value::Object(expected), Value::Object(actual)) => {
            for (key, member) in expected {
                let actual_member = actual
                    .get(key)
                    .ok_or_else(|| mismatch(&format!("{path}.{key}"), member, &Value::Null))?;
                compare_values(
                    member,
                    actual_member,
                    binder,
                    &format!("{path}.{key}"),
                    strict_members,
                )?;
            }
            if strict_members {
                for key in actual.keys() {
                    if !expected.contains_key(key) {
                        return Err(Failed::from(format!("{path}.{key}: unexpected member")));
                    }
                }
            }
            Ok(())
        }
        (Value::Array(expected), Value::Array(actual)) => {
            if expected.len() != actual.len() {
                return Err(Failed::from(format!(
                    "{path}: expected {} item(s), found {}",
                    expected.len(),
                    actual.len()
                )));
            }
            for (index, (member, actual_member)) in expected.iter().zip(actual).enumerate() {
                compare_values(
                    member,
                    actual_member,
                    binder,
                    &format!("{path}[{index}]"),
                    strict_members,
                )?;
            }
            Ok(())
        }
        _ if expected == actual => Ok(()),
        _ => Err(mismatch(path, expected, actual)),
    }
}

/// The one member whose value is a `$generated` label.
fn generated_label(expected: &Value) -> Option<String> {
    let members = expected.as_object()?;
    if members.len() != 1 {
        return None;
    }
    members.get("$generated")?.as_str().map(str::to_string)
}

fn strip_null_members(value: &Value) -> Value {
    match value {
        Value::Object(members) => Value::Object(
            members
                .iter()
                .filter(|(_, member)| !member.is_null())
                .map(|(key, member)| (key.clone(), strip_null_members(member)))
                .collect(),
        ),
        Value::Array(items) => Value::Array(items.iter().map(strip_null_members).collect()),
        other => other.clone(),
    }
}

fn mismatch(path: &str, expected: &Value, actual: &Value) -> Failed {
    Failed::from(format!("{path}: expected {expected}, found {actual}"))
}

fn case_id(case: &Value) -> String {
    case["id"].as_str().unwrap_or("<case>").to_string()
}

fn event_input(value: &Value) -> EventInput {
    let mut input = EventInput::default();
    let Some(members) = value.as_object() else {
        return input;
    };
    input.event = string_field(members, "event").unwrap_or_default();
    input.trail = string_field(members, "trail");
    input.organization_id = string_field(members, "organization_id");
    input.created_at = string_field(members, "created_at");
    input.idempotency_key = string_field(members, "idempotency_key");
    input.schema_version = members
        .get("schema_version")
        .and_then(Value::as_i64)
        .map(|version| version as i32);
    input.actor = members
        .get("actor")
        .and_then(Value::as_object)
        .map(|actor| santati::ActorInput {
            r#type: string_field(actor, "type").unwrap_or_default(),
            id: string_field(actor, "id"),
            name: string_field(actor, "name"),
            metadata: string_map(actor, "metadata"),
        });
    input.targets = members
        .get("targets")
        .and_then(Value::as_array)
        .map(|targets| {
            targets
                .iter()
                .filter_map(Value::as_object)
                .map(|target| santati::TargetInput {
                    r#type: string_field(target, "type").unwrap_or_default(),
                    id: string_field(target, "id").unwrap_or_default(),
                    name: string_field(target, "name"),
                    metadata: string_map(target, "metadata"),
                })
                .collect()
        });
    input.metadata = string_map(members, "metadata");
    input.data = members.get("data").cloned();
    input.context = members
        .get("context")
        .and_then(Value::as_object)
        .map(|context| {
            context
                .iter()
                .map(|(key, value)| (key.clone(), value.clone()))
                .collect()
        });
    input
}

fn list_params(value: &Value) -> ListParams {
    let mut params = ListParams::default();
    let Some(members) = value.as_object() else {
        return params;
    };
    params.trail = string_field(members, "trail");
    params.event = string_field(members, "event");
    params.event_prefix = string_field(members, "event_prefix");
    params.organization_id = string_field(members, "organization_id");
    params.actor_id = string_field(members, "actor_id");
    params.actor_type = string_field(members, "actor_type");
    params.target_type = string_field(members, "target_type");
    params.target_id = string_field(members, "target_id");
    params.created_after = string_field(members, "created_after");
    params.created_before = string_field(members, "created_before");
    params.q = string_field(members, "q");
    params.sort = string_field(members, "sort");
    params.limit = members
        .get("limit")
        .and_then(Value::as_u64)
        .map(|limit| limit as u32);
    params.cursor = string_field(members, "cursor");
    params
}

fn string_field(members: &Map<String, Value>, key: &str) -> Option<String> {
    members.get(key).and_then(Value::as_str).map(str::to_string)
}

fn string_map(members: &Map<String, Value>, key: &str) -> Option<HashMap<String, String>> {
    members.get(key).and_then(Value::as_object).map(|values| {
        values
            .iter()
            .filter_map(|(key, value)| value.as_str().map(|value| (key.clone(), value.to_string())))
            .collect()
    })
}

/// Schema vectors: the page selectors of `params`.
fn page_params(value: &Value) -> PageParams {
    PageParams {
        limit: value
            .get("limit")
            .and_then(Value::as_u64)
            .map(|limit| limit as u32),
        cursor: value
            .get("cursor")
            .and_then(Value::as_str)
            .map(str::to_string),
    }
}

/// A schema vector's `definition` or `changes` member: only the members present.
struct WireDefinition {
    action: Option<String>,
    new_action: Option<String>,
    description: Option<String>,
    allowed_target_types: Option<Vec<String>>,
    is_active: Option<bool>,
}

fn wire_definition(value: &Value) -> WireDefinition {
    let members = value.as_object();
    let text = |key: &str| members.and_then(|m| string_field(m, key));
    WireDefinition {
        action: text("action"),
        new_action: text("new_action"),
        description: text("description"),
        allowed_target_types: members
            .and_then(|m| m.get("allowed_target_types"))
            .and_then(Value::as_array)
            .map(|types| {
                types
                    .iter()
                    .filter_map(|item| item.as_str().map(str::to_string))
                    .collect()
            }),
        is_active: members
            .and_then(|m| m.get("is_active"))
            .and_then(Value::as_bool),
    }
}

async fn collect_all<T: serde::Serialize>(
    stream: impl futures_util::Stream<Item = Result<T, Error>>,
) -> Result<Value, Error> {
    let mut stream = pin!(stream);
    let mut items = Vec::new();
    while let Some(item) = stream.next().await {
        items.push(item?);
    }
    Ok(json!(items))
}

fn version_json(result: santati::SchemaVersionResult) -> Value {
    json!({"schema_version": result.schema_version, "etag": result.etag})
}

/// Run one of the schema operations and render it in the vector's wire shape;
/// `None` when `operation` is not one of them.
async fn schema_operation(
    client: &Santati,
    operation: &str,
    input: &Value,
) -> Option<Result<Value, Error>> {
    let schemas = client.schemas();
    let action = input
        .get("action")
        .and_then(Value::as_str)
        .unwrap_or_default();
    let version = input
        .get("version")
        .and_then(Value::as_i64)
        .unwrap_or_default() as i32;
    let schema = || {
        input
            .get("schema")
            .and_then(Value::as_object)
            .cloned()
            .unwrap_or_default()
    };
    let page = || page_params(&input["params"]);
    Some(match operation {
        "list_definitions" => schemas
            .list_definitions(page())
            .await
            .map(|page| json!({"results": page.results, "next_cursor": page.next_cursor})),
        "iterate_definitions" => collect_all(schemas.iterate_definitions(page())).await,
        "get_definition" => schemas.get_definition(action).await.map(|d| json!(d)),
        "create_definition" => {
            let wire = wire_definition(&input["definition"]);
            schemas
                .create_definition(DefinitionInput {
                    action: wire.action.unwrap_or_default(),
                    description: wire.description,
                    allowed_target_types: wire.allowed_target_types,
                    is_active: wire.is_active,
                })
                .await
                .map(|d| json!(d))
        }
        "update_definition" => {
            let wire = wire_definition(&input["changes"]);
            schemas
                .update_definition(
                    action,
                    DefinitionUpdate {
                        new_action: wire.new_action,
                        description: wire.description,
                        allowed_target_types: wire.allowed_target_types,
                        is_active: wire.is_active,
                    },
                )
                .await
                .map(|d| json!(d))
        }
        "delete_definition" => schemas
            .delete_definition(action)
            .await
            .map(|()| Value::Null),
        "list_versions" => schemas
            .list_versions(action, page())
            .await
            .map(|page| json!({"results": page.results, "next_cursor": page.next_cursor})),
        "iterate_versions" => collect_all(schemas.iterate_versions(action, page())).await,
        "get_version" => schemas.get_version(action, version).await.map(version_json),
        "create_version" => schemas
            .create_version(action, schema())
            .await
            .map(version_json),
        "update_version" => schemas
            .update_version(
                action,
                version,
                schema(),
                input.get("if_match").and_then(Value::as_str),
            )
            .await
            .map(version_json),
        "delete_version" => schemas
            .delete_version(action, version)
            .await
            .map(|()| Value::Null),
        "publish_version" => schemas
            .publish_version(action, version)
            .await
            .map(version_json),
        "deprecate_version" => schemas
            .deprecate_version(action, version)
            .await
            .map(version_json),
        "check_schema" => schemas
            .check_schema(action, schema())
            .await
            .map(|check| json!(check)),
        "list_standard_packs" => schemas
            .list_standard_packs()
            .await
            .map(|catalog| json!(catalog)),
        "install_standard_packs" => {
            let packs = input
                .get("packs")
                .and_then(Value::as_array)
                .map(|packs| {
                    packs
                        .iter()
                        .filter_map(|pack| pack.as_str().map(str::to_string))
                        .collect()
                })
                .unwrap_or_default();
            schemas
                .install_standard_packs(packs)
                .await
                .map(|result| json!(result))
        }
        _ => return None,
    })
}

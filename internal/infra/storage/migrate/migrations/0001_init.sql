-- migrations/0001_init.sql
--
-- v0 schema for fhir-ehr-subscriptions-service.
-- Authority: docs/high-level-design/decisions/{0001, 0008, 0010}.
-- Defines 13 tables, 2 sequences, 2 triggers, partition pre-create for the next 3 months.
--
-- Forward-only migration. Future migrations follow the expand-then-contract discipline
-- documented in docs/low-level-design/storage.md.

create extension if not exists pgcrypto;

create sequence if not exists resource_changes_sequence_seq;

create sequence if not exists ehr_events_event_number_seq;

create table if not exists schema_migrations (
    version text primary key,
    applied_at timestamptz not null default now()
);

comment on table schema_migrations is 'Tracks applied forward-only schema migrations by version.';

create table if not exists auth_clients (
    id text primary key,
    jwks_url text,
    scopes text[],
    display_name text,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);

comment on table auth_clients is 'Registered OAuth/SMART clients permitted to create subscriptions.';

create table if not exists subscription_topics (
    id uuid primary key default gen_random_uuid(),
    url text not null,
    version text not null,
    title text,
    description text,
    status text not null check (status in ('draft', 'active', 'retired')),
    date timestamptz,
    source text not null check (source in ('builtin', 'adapter', 'operator')),
    body jsonb not null,
    compiled_form bytea,
    created_at timestamptz not null default now(),
    retired_at timestamptz,
    unique (url, version)
);

create index if not exists subscription_topics_status_idx on subscription_topics (status);
create index if not exists subscription_topics_url_idx on subscription_topics (url);

comment on table subscription_topics is 'FHIR R5 SubscriptionTopic definitions (built-in, adapter-supplied, or operator-authored).';

create table if not exists subscriptions (
    id uuid primary key default gen_random_uuid(),
    client_id text not null references auth_clients(id),
    status text not null check (status in ('requested', 'active', 'error', 'off', 'entered-in-error')),
    topic_url text not null,
    channel_type text not null,
    endpoint text,
    header jsonb,
    filter_by jsonb,
    content text not null default 'id-only' check (content in ('empty', 'id-only', 'full-resource')),
    heartbeat_period interval,
    timeout interval,
    max_count int not null default 1,
    events_since_subscription_start bigint not null default 0,
    reason text,
    end_time timestamptz,
    error text,
    contact jsonb,
    last_handshake_at timestamptz,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);

create index if not exists subscriptions_topic_status_idx on subscriptions (topic_url, status);

comment on table subscriptions is 'Active and historical FHIR R5 Subscriptions registered by clients.';

create table if not exists hl7_message_queue (
    id uuid primary key default gen_random_uuid(),
    listener_endpoint text not null,
    peer_addr text not null,
    received_at timestamptz not null default now(),
    mllp_message_id text,
    correlation_id uuid not null,
    processed boolean not null default false,
    processed_at timestamptz,
    raw_body bytea not null,
    key_version int not null default 1
);

create index if not exists hl7_message_queue_processed_received_idx on hl7_message_queue (processed, received_at);

comment on table hl7_message_queue is 'Inbound HL7v2 MLLP messages queued for adapter parsing; raw_body is application-encrypted.';

create table if not exists pending_pairs (
    correlation_key text not null,
    listener_endpoint text not null,
    pending_resource bytea not null,
    pending_kind text not null check (pending_kind in ('delete', 'create')),
    source_message_id uuid not null references hl7_message_queue(id) on delete restrict,
    expires_at timestamptz not null,
    created_at timestamptz not null default now(),
    primary key (correlation_key, listener_endpoint)
);

create index if not exists pending_pairs_expires_at_idx on pending_pairs (expires_at);

comment on table pending_pairs is 'Half-pairs awaiting their counterpart (e.g., delete-then-create) for HL7-to-FHIR change reconciliation.';

create table if not exists resource_changes (
    id uuid not null default gen_random_uuid(),
    sequence bigint not null default nextval('resource_changes_sequence_seq'),
    adapter_id text not null,
    correlation_id uuid not null,
    resource_type text not null,
    change_kind text not null check (change_kind in ('create', 'update', 'delete')),
    resource bytea not null,
    previous_resource bytea,
    key_version int not null default 1,
    occurred_at timestamptz not null,
    event_code text,
    processed boolean not null default false,
    created_month date not null,
    created_at timestamptz not null default now(),
    primary key (id, created_month),
    unique (adapter_id, correlation_id, created_month)
) partition by range (created_month);

create index if not exists resource_changes_sequence_idx on resource_changes (sequence);
create index if not exists resource_changes_processed_created_idx on resource_changes (processed, created_at);

comment on table resource_changes is 'Adapter-emitted FHIR resource changes (create/update/delete); partitioned monthly by created_month.';

create table if not exists ehr_events (
    id uuid not null default gen_random_uuid(),
    event_number bigint not null default nextval('ehr_events_event_number_seq'),
    topic_url text not null,
    focus text not null,
    change_kind text not null,
    resource bytea not null,
    previous_resource bytea,
    key_version int not null default 1,
    correlation_id uuid not null,
    occurred_at timestamptz not null,
    notification_shape_hint jsonb,
    resource_change_id uuid not null,
    processed boolean not null default false,
    processed_at timestamptz,
    created_month date not null,
    created_at timestamptz not null default now(),
    primary key (id, created_month),
    unique (event_number, created_month)
) partition by range (created_month);

create index if not exists ehr_events_topic_processed_idx on ehr_events (topic_url, processed);
create index if not exists ehr_events_event_number_idx on ehr_events (event_number);

comment on table ehr_events is 'Topic-matched EHR events fanned out from resource_changes; partitioned monthly by created_month.';

create table if not exists deliveries (
    id uuid primary key default gen_random_uuid(),
    subscription_id uuid not null references subscriptions(id),
    ehr_event_id uuid not null,
    event_number bigint not null,
    status text not null default 'pending' check (status in ('pending', 'delivering', 'delivered', 'failed', 'dead')),
    attempts int not null default 0,
    next_attempt_at timestamptz not null default now(),
    last_error text,
    bundle bytea,
    key_version int not null default 1,
    correlation_id uuid not null,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    unique (subscription_id, event_number)
);

create index if not exists deliveries_status_next_attempt_idx on deliveries (status, next_attempt_at);

comment on table deliveries is 'Per-subscription delivery attempts of notification bundles to channel endpoints.';

create table if not exists dead_letters (
    id uuid primary key default gen_random_uuid(),
    kind text not null check (kind in ('delivery_exhausted', 'hl7_unparseable', 'hl7_invalid_fhir', 'channel_permanent_failure')),
    source_table text not null,
    source_id uuid not null,
    subscription_id uuid references subscriptions(id),
    reason text not null,
    error_detail jsonb,
    payload_redacted bytea,
    correlation_id uuid,
    created_at timestamptz not null default now()
);

create index if not exists dead_letters_kind_created_idx on dead_letters (kind, created_at);

comment on table dead_letters is 'Terminal failures from any pipeline stage; payload_redacted retains evidence with PHI scrubbed.';

create table if not exists adapter_state (
    adapter_id text not null,
    scope text not null,
    key text not null,
    value bytea not null,
    key_version int not null default 1,
    updated_at timestamptz not null default now(),
    primary key (adapter_id, scope, key)
);

comment on table adapter_state is 'Per-adapter durable state (cursors, watermarks, tokens); value is application-encrypted.';

create table if not exists ws_binding_tokens (
    token text primary key,
    subscription_id uuid not null references subscriptions(id) on delete cascade,
    client_id text not null references auth_clients(id),
    expires_at timestamptz not null,
    created_at timestamptz not null default now()
);

create index if not exists ws_binding_tokens_expires_at_idx on ws_binding_tokens (expires_at);

comment on table ws_binding_tokens is 'Single-use bind tokens issued for FHIR R5 WebSocket channel binding.';

create table if not exists audit_log (
    seq bigserial primary key,
    occurred_at timestamptz not null default now(),
    actor_kind text not null check (actor_kind in ('subscriber', 'operator', 'system')),
    actor_id text,
    action text not null,
    target_kind text,
    target_id text,
    outcome text not null check (outcome in ('success', 'failure', 'denied')),
    correlation_id uuid,
    canonical_form bytea not null,
    hash bytea not null,
    prev_hash bytea
);

create index if not exists audit_log_occurred_at_idx on audit_log (occurred_at);
create index if not exists audit_log_actor_id_idx on audit_log (actor_id);
create index if not exists audit_log_correlation_id_idx on audit_log (correlation_id);

comment on table audit_log is 'Append-only hash-chained audit trail; chain integrity is enforced application-side.';

create or replace function set_resource_changes_created_month()
returns trigger
language plpgsql
as $$
begin
    new.created_month := date_trunc('month', now())::date;
    return new;
end;
$$;

drop trigger if exists set_resource_changes_created_month on resource_changes;
create trigger set_resource_changes_created_month
    before insert on resource_changes
    for each row
    execute function set_resource_changes_created_month();

create or replace function set_ehr_events_created_month()
returns trigger
language plpgsql
as $$
begin
    new.created_month := date_trunc('month', now())::date;
    return new;
end;
$$;

drop trigger if exists set_ehr_events_created_month on ehr_events;
create trigger set_ehr_events_created_month
    before insert on ehr_events
    for each row
    execute function set_ehr_events_created_month();

do $$
declare
    n int;
    part_start date;
    part_end date;
    part_suffix text;
begin
    for n in 0..2 loop
        part_start := (date_trunc('month', now()) + (n || ' months')::interval)::date;
        part_end := (date_trunc('month', now()) + ((n + 1) || ' months')::interval)::date;
        part_suffix := to_char(part_start, 'YYYY_MM');

        execute format(
            'create table if not exists resource_changes_%s partition of resource_changes for values from (%L) to (%L)',
            part_suffix, part_start, part_end
        );

        execute format(
            'create table if not exists ehr_events_%s partition of ehr_events for values from (%L) to (%L)',
            part_suffix, part_start, part_end
        );
    end loop;
end;
$$;

insert into schema_migrations (version) values ('0001')
    on conflict (version) do nothing;

-- database/schema.sql
--
-- THE WHOLE DATABASE, in one file, for a brand-new install.
--
--   createdb checkmybond
--   psql -v ON_ERROR_STOP=1 -d checkmybond -f database/schema.sql
--
-- That is all. No migrations to run, no order to get right, no need to
-- start the server first.
--
-- WHAT IT CREATES (26 tables)
--
--   Accounts         users, organizations, password_resets, user_feedback,
--                    login_events
--   Documents        documents, schema_registry, extraction_jobs, field_library,
--   & extraction     template_definitions, bond_sessions, extractions, corrections,
--                    audit_log, chunks, prompt_versions, upload_sessions,
--                    upload_session_files, cleared_sessions_log
--   Billing          plan_transactions, token_grants, coupons, coupon_redemptions
--   Alerting         alert_rules, alert_recipients, alert_events
--
-- and the 17 starting alert rules (see the end of the file). It does NOT
-- create any user: sign up through the app, then see database/README.md for
-- making that first account an admin.
--
-- For a NEW, EMPTY database only. If a table called "users" already exists it
-- stops straight away and changes nothing, so it can't be run by mistake
-- against a database that's in use. To bring an EXISTING database up to date,
-- use the numbered files in migrations/ instead.
--
-- It is one transaction: if anything fails, nothing is left half-built.
--
-- Requires PostgreSQL 13 or newer (password_resets uses the built-in
-- gen_random_uuid()).
--
-- IDENTITY RULE BAKED IN: email is the only unique identity on a user
-- (case-insensitive); the username is a plain display name and is not unique.
-- See migrations/0006_username_not_unique.sql for the reasoning.
--
-- PROVENANCE: generated, not hand-written. It is the exact result of the
-- project's own setup sequence -- the Python models (backend/models/db.py),
-- the Go server's plan_transactions table (hitl-go/internal/planstore), then
-- migrations 0001-0006 -- dumped with pg_dump, so it cannot drift from what
-- the code expects. Whenever you add a migration, regenerate it (see
-- database/README.md).
--
-- Redis and Qdrant are separate services and are not part of this file.

BEGIN;

DO $guard$
BEGIN
    IF to_regclass('public.users') IS NOT NULL
       OR to_regclass('public.plan_transactions') IS NOT NULL THEN
        RAISE EXCEPTION 'This file builds a brand-new database, but a table named users (or plan_transactions) already exists here, so this database is already in use. Create a new empty database and load this file into that. To update an existing database, run the files in migrations/ instead.';
    END IF;
END
$guard$;

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- Name: alert_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.alert_events (
    id character varying NOT NULL,
    rule_id character varying,
    rule_name character varying(128) NOT NULL,
    metric character varying(64) NOT NULL,
    severity character varying(16) NOT NULL,
    comparator character varying(8) NOT NULL,
    threshold double precision NOT NULL,
    observed_value double precision NOT NULL,
    state character varying(16) DEFAULT 'firing'::character varying NOT NULL,
    message text,
    trace_id character varying(64),
    notify_count integer DEFAULT 0 NOT NULL,
    notify_error text,
    started_at timestamp without time zone DEFAULT now() NOT NULL,
    last_notified_at timestamp without time zone,
    resolved_at timestamp without time zone
);


--
-- Name: alert_recipients; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.alert_recipients (
    id character varying NOT NULL,
    email character varying(255) NOT NULL,
    label character varying(128),
    rule_id character varying,
    severity_min character varying(16) DEFAULT 'warning'::character varying NOT NULL,
    is_active boolean DEFAULT true NOT NULL,
    created_at timestamp without time zone DEFAULT now() NOT NULL
);


--
-- Name: alert_rules; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.alert_rules (
    id character varying NOT NULL,
    name character varying(128) NOT NULL,
    description text,
    metric character varying(64) NOT NULL,
    comparator character varying(8) DEFAULT 'gt'::character varying NOT NULL,
    threshold double precision NOT NULL,
    severity character varying(16) DEFAULT 'warning'::character varying NOT NULL,
    window_seconds integer DEFAULT 300 NOT NULL,
    for_seconds integer DEFAULT 60 NOT NULL,
    cooldown_seconds integer DEFAULT 1800 NOT NULL,
    labels text,
    is_active boolean DEFAULT true NOT NULL,
    created_by character varying,
    created_at timestamp without time zone DEFAULT now() NOT NULL,
    updated_at timestamp without time zone DEFAULT now() NOT NULL
);


--
-- Name: audit_log; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.audit_log (
    id integer NOT NULL,
    entry_hash character varying,
    path character varying,
    method character varying(8),
    user_id character varying,
    query_params text,
    duration_ms integer,
    status_code integer,
    body_hash character varying,
    "timestamp" timestamp without time zone
);


--
-- Name: audit_log_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.audit_log_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: audit_log_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.audit_log_id_seq OWNED BY public.audit_log.id;


--
-- Name: bond_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.bond_sessions (
    session_id character varying NOT NULL,
    user_id character varying NOT NULL,
    status character varying,
    documents jsonb,
    selected_field_ids jsonb,
    annotations jsonb,
    template_id character varying,
    extraction_results jsonb,
    reconciliation_result jsonb,
    created_at timestamp without time zone,
    updated_at timestamp without time zone
);


--
-- Name: chunks; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.chunks (
    point_id character varying NOT NULL,
    doc_id character varying,
    doc_hash character varying,
    page_num integer,
    section character varying,
    chunk_type character varying,
    table_type character varying,
    text text,
    created_at timestamp without time zone
);


--
-- Name: cleared_sessions_log; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.cleared_sessions_log (
    id integer NOT NULL,
    session_id character varying NOT NULL,
    user_id character varying,
    total_files integer DEFAULT 0 NOT NULL,
    queued_count integer DEFAULT 0 NOT NULL,
    skipped_count integer DEFAULT 0 NOT NULL,
    failed_count integer DEFAULT 0 NOT NULL,
    session_created_at timestamp without time zone,
    cleared_at timestamp without time zone DEFAULT now() NOT NULL
);


--
-- Name: cleared_sessions_log_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.cleared_sessions_log_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: cleared_sessions_log_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.cleared_sessions_log_id_seq OWNED BY public.cleared_sessions_log.id;


--
-- Name: corrections; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.corrections (
    id character varying NOT NULL,
    extraction_id character varying,
    doc_hash character varying,
    field_name character varying,
    original_value text,
    corrected_value text,
    failure_type character varying,
    analyst_id character varying,
    schema_id character varying,
    prompt_id character varying,
    retrieved_chunk_ids text,
    confidence_score double precision,
    context_relevance double precision,
    answer_relevance double precision,
    groundedness double precision,
    corrected_at timestamp without time zone
);


--
-- Name: coupon_redemptions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.coupon_redemptions (
    id character varying NOT NULL,
    code character varying(64) NOT NULL,
    user_id character varying NOT NULL,
    username character varying(64),
    email character varying(255),
    transaction_id character varying,
    discount_amount numeric(12,2) DEFAULT 0 NOT NULL,
    tokens_granted integer DEFAULT 0 NOT NULL,
    original_amount numeric(12,2) DEFAULT 0 NOT NULL,
    final_amount numeric(12,2) DEFAULT 0 NOT NULL,
    currency character varying(8) DEFAULT 'USD'::character varying NOT NULL,
    redeemed_at timestamp without time zone DEFAULT now() NOT NULL
);


--
-- Name: coupons; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.coupons (
    code character varying(64) NOT NULL,
    kind character varying(16) NOT NULL,
    value numeric(12,2) NOT NULL,
    currency character varying(8) DEFAULT 'USD'::character varying NOT NULL,
    applies_to_plans text,
    min_amount numeric(12,2) DEFAULT 0 NOT NULL,
    max_discount numeric(12,2),
    max_redemptions integer,
    max_redemptions_per_user integer DEFAULT 1 NOT NULL,
    redeemed_count integer DEFAULT 0 NOT NULL,
    starts_at timestamp without time zone DEFAULT now() NOT NULL,
    expires_at timestamp without time zone,
    is_active boolean DEFAULT true NOT NULL,
    batch_id character varying(64),
    campaign character varying(128),
    notes text,
    created_by character varying NOT NULL,
    created_at timestamp without time zone DEFAULT now() NOT NULL,
    updated_at timestamp without time zone DEFAULT now() NOT NULL
);


--
-- Name: documents; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.documents (
    doc_id character varying NOT NULL,
    doc_hash character varying,
    filename character varying,
    pdf_path character varying,
    schema_path character varying,
    status character varying,
    doc_type character varying,
    page_count integer,
    user_id character varying,
    extraction_id character varying,
    error text,
    created_at timestamp without time zone,
    updated_at timestamp without time zone
);


--
-- Name: extraction_jobs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.extraction_jobs (
    extraction_id character varying NOT NULL,
    doc_hash character varying,
    schema_id character varying,
    pdf_path character varying,
    parsed_content_json text,
    status character varying,
    result_json text,
    error text,
    created_at timestamp without time zone,
    started_at timestamp without time zone,
    completed_at timestamp without time zone,
    sla_deadline timestamp without time zone NOT NULL
);


--
-- Name: extractions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.extractions (
    extraction_id character varying NOT NULL,
    doc_id character varying,
    schema_id character varying,
    status character varying,
    fields_json text,
    auto_accepted integer,
    queued_for_review integer,
    failed_count integer,
    overall_confidence double precision,
    error text,
    created_at timestamp without time zone,
    started_at timestamp without time zone,
    completed_at timestamp without time zone
);


--
-- Name: field_library; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.field_library (
    id character varying NOT NULL,
    name character varying NOT NULL,
    description text,
    field_type character varying(32) NOT NULL,
    aliases jsonb,
    category character varying(32),
    is_active boolean,
    created_at timestamp without time zone,
    updated_at timestamp without time zone
);


--
-- Name: login_events; Type: TABLE; Schema: public; Owner: -
--
-- One row per successful login, so daily/weekly/monthly active users can be
-- charted historically (users.last_login is overwritten on every login).
-- No FK to users(id) on purpose: removing an account must not block, and the
-- history stays intact. Written by hitl-go/internal/db UpdateLastLogin().
-- Queries: database/usage_queries.sql

CREATE TABLE public.login_events (
    id integer NOT NULL,
    user_id character varying NOT NULL,
    logged_in_at timestamp without time zone DEFAULT now() NOT NULL
);


CREATE SEQUENCE public.login_events_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER SEQUENCE public.login_events_id_seq OWNED BY public.login_events.id;

ALTER TABLE ONLY public.login_events ALTER COLUMN id SET DEFAULT nextval('public.login_events_id_seq'::regclass);

ALTER TABLE ONLY public.login_events
    ADD CONSTRAINT login_events_pkey PRIMARY KEY (id);

CREATE INDEX ix_login_events_logged_in_at ON public.login_events USING btree (logged_in_at);

CREATE INDEX ix_login_events_user_id_logged_in_at ON public.login_events USING btree (user_id, logged_in_at);


--
-- Name: organizations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.organizations (
    org_id character varying NOT NULL,
    org_name character varying NOT NULL,
    created_at timestamp without time zone
);


--
-- Name: password_resets; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.password_resets (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    email text NOT NULL,
    token_hash text NOT NULL,
    expires_at timestamp without time zone NOT NULL,
    created_at timestamp without time zone DEFAULT now()
);


--
-- Name: plan_transactions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.plan_transactions (
    id text NOT NULL,
    user_id text NOT NULL,
    username text,
    email text,
    plan_id text NOT NULL,
    plan_label text NOT NULL,
    quantity integer NOT NULL,
    fields_allotted integer NOT NULL,
    price_per_unit numeric(10,2) NOT NULL,
    total_amount numeric(10,2) NOT NULL,
    currency text DEFAULT 'USD'::text NOT NULL,
    payment_gateway text NOT NULL,
    gateway_transaction_id text,
    status text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    refunded_amount numeric(12,2) DEFAULT 0 NOT NULL,
    refunded_at timestamp with time zone,
    coupon_code character varying(64),
    discount_amount numeric(12,2) DEFAULT 0 NOT NULL
);


--
-- Name: prompt_versions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.prompt_versions (
    prompt_id character varying NOT NULL,
    prompt_name character varying,
    version_tag character varying,
    body text,
    created_at timestamp without time zone,
    created_by character varying,
    is_active boolean
);


--
-- Name: schema_registry; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.schema_registry (
    id integer NOT NULL,
    schema_id character varying NOT NULL,
    schema_name character varying NOT NULL,
    version character varying NOT NULL,
    definition_json text NOT NULL,
    created_at timestamp without time zone,
    created_by character varying
);


--
-- Name: schema_registry_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.schema_registry_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: schema_registry_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.schema_registry_id_seq OWNED BY public.schema_registry.id;


--
-- Name: template_definitions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.template_definitions (
    template_id character varying NOT NULL,
    org_id character varying NOT NULL,
    template_name character varying,
    bank_name character varying NOT NULL,
    document_type character varying NOT NULL,
    fingerprint_hash character varying NOT NULL,
    fingerprint_json jsonb NOT NULL,
    rules_json jsonb NOT NULL,
    field_ids_json jsonb,
    created_by character varying NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    last_used_at timestamp with time zone,
    success_count integer,
    fail_count integer,
    is_active boolean
);


--
-- Name: token_grants; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.token_grants (
    id text NOT NULL,
    user_id text NOT NULL,
    transaction_id text,
    plan_id text NOT NULL,
    source text NOT NULL,
    fields_granted integer NOT NULL,
    fields_remaining integer NOT NULL,
    granted_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone,
    expired_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT token_grants_fields_granted_check CHECK ((fields_granted > 0)),
    CONSTRAINT token_grants_fields_remaining_check CHECK ((fields_remaining >= 0))
);


--
-- Name: upload_session_files; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.upload_session_files (
    id character varying NOT NULL,
    session_id character varying NOT NULL,
    filename character varying NOT NULL,
    doc_hash character varying,
    file_index integer NOT NULL,
    status character varying,
    extraction_id character varying,
    error text,
    size_bytes integer,
    processed_at timestamp without time zone
);


--
-- Name: upload_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.upload_sessions (
    session_id character varying NOT NULL,
    user_id character varying NOT NULL,
    schema_id character varying,
    total_files integer NOT NULL,
    queued_count integer,
    skipped_count integer,
    failed_count integer,
    status character varying,
    created_at timestamp without time zone,
    updated_at timestamp without time zone
);


--
-- Name: user_feedback; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_feedback (
    id character varying NOT NULL,
    user_id character varying NOT NULL,
    username character varying(64),
    kind character varying(32) DEFAULT 'feedback'::character varying NOT NULL,
    message text NOT NULL,
    rating integer,
    created_at timestamp without time zone DEFAULT now() NOT NULL
);


--
-- Name: users; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.users (
    id character varying NOT NULL,
    org_id character varying,
    username character varying(64) NOT NULL,
    email character varying(255) NOT NULL,
    password_hash character varying(128) NOT NULL,
    role character varying(32) NOT NULL,
    is_active boolean,
    created_at timestamp without time zone,
    last_login timestamp without time zone,
    last_logout timestamp without time zone,
    deleted_at timestamp without time zone,
    region character varying(128),
    token_balance integer NOT NULL
);


--
-- Name: audit_log id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.audit_log ALTER COLUMN id SET DEFAULT nextval('public.audit_log_id_seq'::regclass);


--
-- Name: cleared_sessions_log id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.cleared_sessions_log ALTER COLUMN id SET DEFAULT nextval('public.cleared_sessions_log_id_seq'::regclass);


--
-- Name: schema_registry id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.schema_registry ALTER COLUMN id SET DEFAULT nextval('public.schema_registry_id_seq'::regclass);


--
-- Name: alert_events alert_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.alert_events
    ADD CONSTRAINT alert_events_pkey PRIMARY KEY (id);


--
-- Name: alert_recipients alert_recipients_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.alert_recipients
    ADD CONSTRAINT alert_recipients_pkey PRIMARY KEY (id);


--
-- Name: alert_rules alert_rules_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.alert_rules
    ADD CONSTRAINT alert_rules_name_key UNIQUE (name);


--
-- Name: alert_rules alert_rules_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.alert_rules
    ADD CONSTRAINT alert_rules_pkey PRIMARY KEY (id);


--
-- Name: audit_log audit_log_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.audit_log
    ADD CONSTRAINT audit_log_pkey PRIMARY KEY (id);


--
-- Name: bond_sessions bond_sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bond_sessions
    ADD CONSTRAINT bond_sessions_pkey PRIMARY KEY (session_id);


--
-- Name: chunks chunks_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.chunks
    ADD CONSTRAINT chunks_pkey PRIMARY KEY (point_id);


--
-- Name: cleared_sessions_log cleared_sessions_log_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.cleared_sessions_log
    ADD CONSTRAINT cleared_sessions_log_pkey PRIMARY KEY (id);


--
-- Name: corrections corrections_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.corrections
    ADD CONSTRAINT corrections_pkey PRIMARY KEY (id);


--
-- Name: coupon_redemptions coupon_redemptions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.coupon_redemptions
    ADD CONSTRAINT coupon_redemptions_pkey PRIMARY KEY (id);


--
-- Name: coupons coupons_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.coupons
    ADD CONSTRAINT coupons_pkey PRIMARY KEY (code);


--
-- Name: documents documents_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.documents
    ADD CONSTRAINT documents_pkey PRIMARY KEY (doc_id);


--
-- Name: extraction_jobs extraction_jobs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.extraction_jobs
    ADD CONSTRAINT extraction_jobs_pkey PRIMARY KEY (extraction_id);


--
-- Name: extractions extractions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.extractions
    ADD CONSTRAINT extractions_pkey PRIMARY KEY (extraction_id);


--
-- Name: field_library field_library_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.field_library
    ADD CONSTRAINT field_library_pkey PRIMARY KEY (id);


--
-- Name: organizations organizations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.organizations
    ADD CONSTRAINT organizations_pkey PRIMARY KEY (org_id);


--
-- Name: password_resets password_resets_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.password_resets
    ADD CONSTRAINT password_resets_pkey PRIMARY KEY (id);


--
-- Name: password_resets password_resets_token_hash_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.password_resets
    ADD CONSTRAINT password_resets_token_hash_key UNIQUE (token_hash);


--
-- Name: plan_transactions plan_transactions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.plan_transactions
    ADD CONSTRAINT plan_transactions_pkey PRIMARY KEY (id);


--
-- Name: prompt_versions prompt_versions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.prompt_versions
    ADD CONSTRAINT prompt_versions_pkey PRIMARY KEY (prompt_id);


--
-- Name: schema_registry schema_registry_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.schema_registry
    ADD CONSTRAINT schema_registry_pkey PRIMARY KEY (id);


--
-- Name: template_definitions template_definitions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.template_definitions
    ADD CONSTRAINT template_definitions_pkey PRIMARY KEY (template_id);


--
-- Name: token_grants token_grants_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.token_grants
    ADD CONSTRAINT token_grants_pkey PRIMARY KEY (id);


--
-- Name: upload_session_files upload_session_files_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.upload_session_files
    ADD CONSTRAINT upload_session_files_pkey PRIMARY KEY (id);


--
-- Name: upload_sessions upload_sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.upload_sessions
    ADD CONSTRAINT upload_sessions_pkey PRIMARY KEY (session_id);


--
-- Name: users uq_users_email; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT uq_users_email UNIQUE (email);


--
-- Name: user_feedback user_feedback_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_feedback
    ADD CONSTRAINT user_feedback_pkey PRIMARY KEY (id);


--
-- Name: users users_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_pkey PRIMARY KEY (id);


--
-- Name: idx_alert_events_rule; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_alert_events_rule ON public.alert_events USING btree (rule_id);


--
-- Name: idx_alert_events_started; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_alert_events_started ON public.alert_events USING btree (started_at DESC);


--
-- Name: idx_alert_events_state; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_alert_events_state ON public.alert_events USING btree (state);


--
-- Name: idx_alert_recipients_active; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_alert_recipients_active ON public.alert_recipients USING btree (is_active);


--
-- Name: idx_alert_recipients_rule; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_alert_recipients_rule ON public.alert_recipients USING btree (rule_id);


--
-- Name: idx_alert_rules_active; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_alert_rules_active ON public.alert_rules USING btree (is_active);


--
-- Name: idx_coupon_redemptions_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_coupon_redemptions_at ON public.coupon_redemptions USING btree (redeemed_at);


--
-- Name: idx_coupon_redemptions_code; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_coupon_redemptions_code ON public.coupon_redemptions USING btree (code);


--
-- Name: idx_coupon_redemptions_code_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_coupon_redemptions_code_user ON public.coupon_redemptions USING btree (code, user_id);


--
-- Name: idx_coupon_redemptions_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_coupon_redemptions_user ON public.coupon_redemptions USING btree (user_id);


--
-- Name: idx_coupons_active; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_coupons_active ON public.coupons USING btree (is_active, expires_at);


--
-- Name: idx_coupons_batch_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_coupons_batch_id ON public.coupons USING btree (batch_id);


--
-- Name: idx_coupons_campaign; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_coupons_campaign ON public.coupons USING btree (campaign);


--
-- Name: idx_plan_transactions_coupon; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_plan_transactions_coupon ON public.plan_transactions USING btree (coupon_code);


--
-- Name: idx_templates_org_active; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_templates_org_active ON public.template_definitions USING btree (org_id, is_active, last_used_at);


--
-- Name: idx_templates_org_fingerprint; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_templates_org_fingerprint ON public.template_definitions USING btree (org_id, bank_name, document_type, fingerprint_hash);


--
-- Name: idx_token_grants_consumption_order; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_token_grants_consumption_order ON public.token_grants USING btree (user_id, expires_at, granted_at) WHERE ((fields_remaining > 0) AND (expired_at IS NULL));


--
-- Name: idx_token_grants_pending_expiry; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_token_grants_pending_expiry ON public.token_grants USING btree (expires_at) WHERE ((expires_at IS NOT NULL) AND (expired_at IS NULL));


--
-- Name: idx_token_grants_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_token_grants_user ON public.token_grants USING btree (user_id);


--
-- Name: idx_user_feedback_kind; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_feedback_kind ON public.user_feedback USING btree (kind);


--
-- Name: idx_user_feedback_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_feedback_user_id ON public.user_feedback USING btree (user_id);


--
-- Name: ix_audit_log_entry_hash; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX ix_audit_log_entry_hash ON public.audit_log USING btree (entry_hash);


--
-- Name: ix_audit_log_timestamp; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_audit_log_timestamp ON public.audit_log USING btree ("timestamp");


--
-- Name: ix_audit_log_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_audit_log_user_id ON public.audit_log USING btree (user_id);


--
-- Name: ix_bond_sessions_template_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_bond_sessions_template_id ON public.bond_sessions USING btree (template_id);


--
-- Name: ix_bond_sessions_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_bond_sessions_user_id ON public.bond_sessions USING btree (user_id);


--
-- Name: ix_chunks_doc_hash; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_chunks_doc_hash ON public.chunks USING btree (doc_hash);


--
-- Name: ix_chunks_doc_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_chunks_doc_id ON public.chunks USING btree (doc_id);


--
-- Name: ix_corrections_analyst_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_corrections_analyst_id ON public.corrections USING btree (analyst_id);


--
-- Name: ix_corrections_doc_hash; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_corrections_doc_hash ON public.corrections USING btree (doc_hash);


--
-- Name: ix_corrections_extraction_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_corrections_extraction_id ON public.corrections USING btree (extraction_id);


--
-- Name: ix_corrections_failure_type; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_corrections_failure_type ON public.corrections USING btree (failure_type);


--
-- Name: ix_corrections_field_name; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_corrections_field_name ON public.corrections USING btree (field_name);


--
-- Name: ix_corrections_prompt_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_corrections_prompt_id ON public.corrections USING btree (prompt_id);


--
-- Name: ix_corrections_schema_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_corrections_schema_id ON public.corrections USING btree (schema_id);


--
-- Name: ix_documents_doc_hash; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_documents_doc_hash ON public.documents USING btree (doc_hash);


--
-- Name: ix_documents_extraction_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_documents_extraction_id ON public.documents USING btree (extraction_id);


--
-- Name: ix_documents_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_documents_user_id ON public.documents USING btree (user_id);


--
-- Name: ix_extraction_jobs_doc_hash; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_extraction_jobs_doc_hash ON public.extraction_jobs USING btree (doc_hash);


--
-- Name: ix_extraction_jobs_schema_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_extraction_jobs_schema_id ON public.extraction_jobs USING btree (schema_id);


--
-- Name: ix_extractions_doc_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_extractions_doc_id ON public.extractions USING btree (doc_id);


--
-- Name: ix_extractions_schema_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_extractions_schema_id ON public.extractions USING btree (schema_id);


--
-- Name: ix_field_library_name; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_field_library_name ON public.field_library USING btree (name);


--
-- Name: ix_prompt_versions_prompt_name; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_prompt_versions_prompt_name ON public.prompt_versions USING btree (prompt_name);


--
-- Name: ix_schema_registry_schema_id; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX ix_schema_registry_schema_id ON public.schema_registry USING btree (schema_id);


--
-- Name: ix_template_definitions_org_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_template_definitions_org_id ON public.template_definitions USING btree (org_id);


--
-- Name: ix_users_email; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_users_email ON public.users USING btree (email);


--
-- Name: ix_users_org_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_users_org_id ON public.users USING btree (org_id);


--
-- Name: ix_users_username; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_users_username ON public.users USING btree (username);


--
-- Name: ux_plan_transactions_free_claim; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX ux_plan_transactions_free_claim ON public.plan_transactions USING btree (user_id) WHERE ((plan_id = 'free'::text) AND (status = 'succeeded'::text));


--
-- Name: ux_users_email_lower; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX ux_users_email_lower ON public.users USING btree (lower((email)::text));


--
-- Name: alert_events alert_events_rule_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.alert_events
    ADD CONSTRAINT alert_events_rule_id_fkey FOREIGN KEY (rule_id) REFERENCES public.alert_rules(id) ON DELETE SET NULL;


--
-- Name: alert_recipients alert_recipients_rule_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.alert_recipients
    ADD CONSTRAINT alert_recipients_rule_id_fkey FOREIGN KEY (rule_id) REFERENCES public.alert_rules(id) ON DELETE CASCADE;


--
-- Name: bond_sessions bond_sessions_template_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bond_sessions
    ADD CONSTRAINT bond_sessions_template_id_fkey FOREIGN KEY (template_id) REFERENCES public.template_definitions(template_id);


--
-- Name: bond_sessions bond_sessions_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bond_sessions
    ADD CONSTRAINT bond_sessions_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id);


--
-- Name: chunks chunks_doc_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.chunks
    ADD CONSTRAINT chunks_doc_id_fkey FOREIGN KEY (doc_id) REFERENCES public.documents(doc_id);


--
-- Name: corrections corrections_analyst_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.corrections
    ADD CONSTRAINT corrections_analyst_id_fkey FOREIGN KEY (analyst_id) REFERENCES public.users(id);


--
-- Name: coupon_redemptions coupon_redemptions_code_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.coupon_redemptions
    ADD CONSTRAINT coupon_redemptions_code_fkey FOREIGN KEY (code) REFERENCES public.coupons(code) ON DELETE CASCADE;


--
-- Name: documents documents_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.documents
    ADD CONSTRAINT documents_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id);


--
-- Name: extractions extractions_doc_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.extractions
    ADD CONSTRAINT extractions_doc_id_fkey FOREIGN KEY (doc_id) REFERENCES public.documents(doc_id);


--
-- Name: extractions extractions_schema_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.extractions
    ADD CONSTRAINT extractions_schema_id_fkey FOREIGN KEY (schema_id) REFERENCES public.schema_registry(schema_id);


--
-- Name: prompt_versions prompt_versions_created_by_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.prompt_versions
    ADD CONSTRAINT prompt_versions_created_by_fkey FOREIGN KEY (created_by) REFERENCES public.users(id);


--
-- Name: schema_registry schema_registry_created_by_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.schema_registry
    ADD CONSTRAINT schema_registry_created_by_fkey FOREIGN KEY (created_by) REFERENCES public.users(id);


--
-- Name: template_definitions template_definitions_created_by_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.template_definitions
    ADD CONSTRAINT template_definitions_created_by_fkey FOREIGN KEY (created_by) REFERENCES public.users(id);


--
-- Name: template_definitions template_definitions_org_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.template_definitions
    ADD CONSTRAINT template_definitions_org_id_fkey FOREIGN KEY (org_id) REFERENCES public.organizations(org_id);


--
-- Name: token_grants token_grants_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.token_grants
    ADD CONSTRAINT token_grants_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id);


--
-- Name: upload_session_files upload_session_files_session_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.upload_session_files
    ADD CONSTRAINT upload_session_files_session_id_fkey FOREIGN KEY (session_id) REFERENCES public.upload_sessions(session_id);


--
-- Name: upload_sessions upload_sessions_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.upload_sessions
    ADD CONSTRAINT upload_sessions_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id);


--
-- Name: users users_org_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_org_id_fkey FOREIGN KEY (org_id) REFERENCES public.organizations(org_id);


--
-- PostgreSQL database dump complete
--

-- ===========================================================================
-- Starting data: the alert rules (copied from migration 0004)
-- ===========================================================================

-- ── Seed: a starting rule set ───────────────────────────────────────────────
-- These are deliberately conservative starting thresholds, not tuned
-- numbers — tune them against a week of real traffic before trusting the
-- pager. Every one is editable from the Alerts admin UI.
INSERT INTO alert_rules (id, name, description, metric, comparator, threshold, severity, window_seconds, for_seconds, cooldown_seconds, created_by)
VALUES
  ('rule_api_5xx',      'API 5xx error rate',      'Share of requests returning 5xx across all routes.',                 'http_error_rate_pct',        'gt',  5,     'critical', 300,  120, 1800, 'seed'),
  ('rule_api_latency',  'API p95 latency',         'p95 request latency across all routes, in milliseconds.',            'http_latency_p95_ms',        'gt',  2000,  'warning',  300,  180, 1800, 'seed'),
  ('rule_db_down',      'Database unreachable',    'Postgres ping fails. 1 = reachable, 0 = down.',                      'db_up',                      'lt',  1,     'critical', 60,   0,   600,  'seed'),
  ('rule_db_latency',   'Database query latency',  'p95 time spent inside Postgres queries, in milliseconds.',           'db_query_p95_ms',            'gt',  500,   'warning',  300,  180, 1800, 'seed'),
  ('rule_redis_down',   'Redis unreachable',       'Redis ping fails. 1 = reachable, 0 = down.',                         'redis_up',                   'lt',  1,     'critical', 60,   0,   600,  'seed'),
  ('rule_queue_depth',  'Extraction queue backlog','Jobs sitting in pending or processing.',                             'queue_depth',                'gt',  100,   'warning',  60,   300, 3600, 'seed'),
  ('rule_sla_breach',   'SLA breaches',            'Jobs past their SLA deadline and still not complete.',               'sla_breach_count',           'gt',  5,     'critical', 60,   60,  1800, 'seed'),
  ('rule_extract_fail', 'Extraction failure rate', 'Share of extraction jobs ending in failed status.',                  'extraction_failure_rate_pct','gt',  20,    'critical', 900,  300, 1800, 'seed'),
  ('rule_payment_fail', 'Payment failure rate',    'Share of plan_transactions rows with status failed.',                'payment_failure_rate_pct',   'gt',  25,    'critical', 900,  120, 1800, 'seed'),
  ('rule_login_fail',   'Login failure spike',     'Failed auth responses per minute — brute force or a broken login.',  'auth_failure_rate_per_min',  'gt',  30,    'warning',  300,  120, 1800, 'seed'),
  ('rule_coupon_abuse', 'Coupon redemption spike', 'Coupon redemptions per minute — abuse or a leaked code.',            'coupon_redemptions_per_min', 'gt',  20,    'warning',  300,  120, 3600, 'seed'),
  ('rule_confidence',   'Extraction confidence drift','Drop in mean confidence vs the prior baseline week, in points.', 'confidence_drift',           'gt',  0.1,   'warning',  86400,0,   86400,'seed')
ON CONFLICT (id) DO NOTHING;

-- ── Seed: extra latency / throughput / performance-decline rules ────────────
-- (copied from migration 0007). Written for a product whose normal traffic
-- is not known yet: the first four don't depend on traffic volume and are
-- ACTIVE; the throughput-drop rule is INACTIVE until you've measured a
-- baseline, because any number picked blindly either spams you at night or
-- never fires.
--
-- To pick the throughput threshold after ~7 days of real traffic:
--   SELECT date_trunc('hour', timestamp) AS hour,
--          round(count(*) / 60.0, 2)     AS avg_req_per_min
--   FROM audit_log
--   WHERE timestamp >= now() - interval '7 days'
--   GROUP BY 1 ORDER BY 1;
-- Set the rule's threshold to about half the quietest normal hour in
-- /admin/alerts, then switch it on.
INSERT INTO alert_rules (id, name, description, metric, comparator, threshold, severity, window_seconds, for_seconds, cooldown_seconds, is_active, created_by)
VALUES
  ('rule_oldest_pending',  'Stuck extraction job',       'Oldest unfinished extraction job has been waiting longer than this many minutes.', 'oldest_pending_job_minutes', 'gt', 60,   'critical', 60,   120, 3600, true,  'seed7'),
  ('rule_extract_slow',    'Extraction p95 duration',    '95th percentile extraction time (seconds) over the last 30 min.',                  'extraction_p95_seconds',     'gt', 300,  'warning',  1800, 300, 3600, true,  'seed7'),
  ('rule_db_pool',         'DB connection pool saturated','Connections in use / pool max (0-1). Sustained high = every endpoint is waiting.', 'db_pool_utilization',       'gt', 0.85, 'warning',  120,  120, 1800, true,  'seed7'),
  ('rule_api_latency_p99', 'DB query p99 latency',       'p99 time inside Postgres queries (ms).',                                           'db_query_p99_ms',            'gt', 1500, 'warning',  300,  180, 1800, true,  'seed7'),
  ('rule_throughput_drop', 'Request volume dropped',     'Requests per minute (from audit_log) fell below the threshold. Set threshold to ~30-50% of your normal off-peak rate, then activate.', 'http_requests_per_min', 'lt', 1, 'warning', 900, 300, 3600, false, 'seed7')
ON CONFLICT (id) DO NOTHING;

COMMIT;

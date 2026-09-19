--
-- PostgreSQL database dump
--

\restrict OcycDndAtiFfv4II1rfClnQVkJ4Exy2Qv9qMfGAtpAHbwqDbH8qRPKAwYXUl4Xu

-- Dumped from database version 16.12
-- Dumped by pg_dump version 16.15 (Ubuntu 16.15-0ubuntu0.24.04.1)

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

--
-- Name: uuid-ossp; Type: EXTENSION; Schema: -; Owner: -
--

CREATE EXTENSION IF NOT EXISTS "uuid-ossp" WITH SCHEMA public;


--
-- Name: EXTENSION "uuid-ossp"; Type: COMMENT; Schema: -; Owner: 
--

COMMENT ON EXTENSION "uuid-ossp" IS 'generate universally unique identifiers (UUIDs)';


--
-- Name: current_user_id(); Type: FUNCTION; Schema: public; Owner: omnira
--

CREATE FUNCTION public.current_user_id() RETURNS uuid
    LANGUAGE sql STABLE
    AS $$
  SELECT current_setting('app.current_user_id', TRUE)::UUID;
$$;


ALTER FUNCTION public.current_user_id() OWNER TO omnira;

--
-- Name: is_system_admin(); Type: FUNCTION; Schema: public; Owner: omnira
--

CREATE FUNCTION public.is_system_admin() RETURNS boolean
    LANGUAGE sql STABLE
    AS $$
  SELECT current_setting('app.is_system_admin', TRUE)::BOOLEAN;
$$;


ALTER FUNCTION public.is_system_admin() OWNER TO omnira;

SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- Name: audit_events; Type: TABLE; Schema: public; Owner: omnira
--

CREATE TABLE public.audit_events (
    id uuid DEFAULT public.uuid_generate_v4() NOT NULL,
    tenant_id uuid,
    actor_id uuid,
    action text NOT NULL,
    resource_type text NOT NULL,
    resource_id text,
    outcome text NOT NULL,
    correlation_id text,
    metadata jsonb DEFAULT '{}'::jsonb,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT audit_events_outcome_check CHECK ((outcome = ANY (ARRAY['success'::text, 'failure'::text])))
);


ALTER TABLE public.audit_events OWNER TO omnira;

--
-- Name: memberships; Type: TABLE; Schema: public; Owner: omnira
--

CREATE TABLE public.memberships (
    id uuid DEFAULT public.uuid_generate_v4() NOT NULL,
    tenant_id uuid NOT NULL,
    user_id uuid NOT NULL,
    role_id uuid NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT memberships_status_check CHECK ((status = ANY (ARRAY['active'::text, 'inactive'::text, 'revoked'::text])))
);


ALTER TABLE public.memberships OWNER TO omnira;

--
-- Name: outbox_events; Type: TABLE; Schema: public; Owner: omnira
--

CREATE TABLE public.outbox_events (
    id uuid DEFAULT public.uuid_generate_v4() NOT NULL,
    tenant_id uuid,
    event_type text NOT NULL,
    aggregate_type text NOT NULL,
    aggregate_id text NOT NULL,
    correlation_id text,
    causation_id text,
    payload jsonb NOT NULL,
    occurred_at timestamp with time zone DEFAULT now() NOT NULL,
    published_at timestamp with time zone,
    attempts integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


ALTER TABLE public.outbox_events OWNER TO omnira;

--
-- Name: permissions; Type: TABLE; Schema: public; Owner: omnira
--

CREATE TABLE public.permissions (
    key text NOT NULL,
    description text
);


ALTER TABLE public.permissions OWNER TO omnira;

--
-- Name: role_permissions; Type: TABLE; Schema: public; Owner: omnira
--

CREATE TABLE public.role_permissions (
    role_id uuid NOT NULL,
    permission_key text NOT NULL
);


ALTER TABLE public.role_permissions OWNER TO omnira;

--
-- Name: roles; Type: TABLE; Schema: public; Owner: omnira
--

CREATE TABLE public.roles (
    id uuid DEFAULT public.uuid_generate_v4() NOT NULL,
    tenant_id uuid,
    key text NOT NULL,
    name text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


ALTER TABLE public.roles OWNER TO omnira;

--
-- Name: tenants; Type: TABLE; Schema: public; Owner: omnira
--

CREATE TABLE public.tenants (
    id uuid DEFAULT public.uuid_generate_v4() NOT NULL,
    legal_name text NOT NULL,
    trade_name text,
    tax_id text,
    isolation_profile text DEFAULT 'shared_strong_isolation'::text NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT tenants_isolation_profile_check CHECK ((isolation_profile = ANY (ARRAY['shared_strong_isolation'::text, 'dedicated_database'::text]))),
    CONSTRAINT tenants_status_check CHECK ((status = ANY (ARRAY['active'::text, 'inactive'::text, 'suspended'::text])))
);


ALTER TABLE public.tenants OWNER TO omnira;

--
-- Name: users; Type: TABLE; Schema: public; Owner: omnira
--

CREATE TABLE public.users (
    id uuid DEFAULT public.uuid_generate_v4() NOT NULL,
    external_subject text NOT NULL,
    email text,
    display_name text,
    status text DEFAULT 'active'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT users_status_check CHECK ((status = ANY (ARRAY['active'::text, 'inactive'::text])))
);


ALTER TABLE public.users OWNER TO omnira;

--
-- Data for Name: audit_events; Type: TABLE DATA; Schema: public; Owner: omnira
--

COPY public.audit_events (id, tenant_id, actor_id, action, resource_type, resource_id, outcome, correlation_id, metadata, created_at) FROM stdin;
\.


--
-- Data for Name: memberships; Type: TABLE DATA; Schema: public; Owner: omnira
--

COPY public.memberships (id, tenant_id, user_id, role_id, status, created_at, updated_at) FROM stdin;
a7c6cc65-904a-4a00-b2da-63471b7fd3bc	a8a70035-805b-4add-972d-144778c5ae17	585f7f12-14c6-4d8b-9b6d-53ae571b5493	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 17:58:59.965793+00	2026-09-18 17:58:59.965793+00
ee69a871-3608-458c-a6cf-37fbaca2f04d	91c57e4f-a7aa-4b73-8899-c4552110ec0b	259a4c79-d79e-41f1-881c-dc66093c93b7	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 17:59:03.309996+00	2026-09-18 17:59:03.309996+00
82598cb6-bef1-474b-b23d-4e34d3b65be2	c9a96f9b-c9ab-4c30-9e91-6def6295a9ff	8dacffce-da9d-4960-95fd-34da055a166c	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 17:59:03.331234+00	2026-09-18 17:59:03.331234+00
4ec4872f-7ae1-4448-b645-fda77ce10fe3	706eb126-d928-4196-993e-1dfe58c8050f	0f22be05-6d45-4e6f-84d1-671a59556984	796f5977-ae7c-418f-afa8-44f66724e640	revoked	2026-09-18 17:59:03.349126+00	2026-09-18 17:59:03.353393+00
540dfb80-f0e0-4e5a-9b35-1d6d66a9e4e1	8ad2be27-a2ec-4f06-a1eb-89c7c3978fae	e40d6474-508d-43e1-bba5-fd8164944eb9	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 17:59:03.374976+00	2026-09-18 17:59:03.374976+00
f262cf02-2e44-4538-a06c-f848403edfeb	8ad2be27-a2ec-4f06-a1eb-89c7c3978fae	db666020-7b24-4316-9806-b5f06b92eed9	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 17:59:03.381826+00	2026-09-18 17:59:03.381826+00
aa8079db-ce80-48b3-b254-6feab373ac2f	ebab7ce3-02c7-40d2-ba3c-fb42da7a48b2	be48471f-4a86-4b2a-9f60-da9bc9d8d85b	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 17:59:03.388053+00	2026-09-18 17:59:03.388053+00
726384ba-eb04-4210-8ae5-64cf803e7a61	ebab7ce3-02c7-40d2-ba3c-fb42da7a48b2	62facbcb-3d19-480e-811a-b2965c4a354f	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 17:59:03.393949+00	2026-09-18 17:59:03.393949+00
1721681b-dd4f-4095-a85e-29c5f61b81b5	a38cf23f-c783-4bc5-8bb9-85df519a5cc9	d4036bd5-e571-4525-8e5d-3e9d8466ac9d	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 17:59:03.399896+00	2026-09-18 17:59:03.399896+00
bb4f0a7e-e4c6-4a5d-a809-0214d11f53f7	a38cf23f-c783-4bc5-8bb9-85df519a5cc9	0f20daa4-44fd-4184-8b97-52efe33e1b06	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 17:59:03.406022+00	2026-09-18 17:59:03.406022+00
f6c58c3f-ad7b-42d3-b6af-f6d651bdd4a5	6d3efbe8-4575-473e-824b-da5257d04478	4fbf70bf-5ced-43d3-81f9-23197cb15c7e	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 17:59:06.904552+00	2026-09-18 17:59:06.904552+00
95bbc100-bfb0-4ce6-8d41-936991f1634d	1b4b0b07-290e-4a77-ab0f-d91fa5e2fafb	5a58f1f9-37d2-4774-a388-e8d1a41f76c5	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 17:59:06.924515+00	2026-09-18 17:59:06.924515+00
cdb9e45a-bc45-47c5-8874-9719f47baf51	2c717d4b-2305-4cb1-8794-5c0911443b2d	da4256db-5246-490e-95bb-7994cd01324d	796f5977-ae7c-418f-afa8-44f66724e640	revoked	2026-09-18 17:59:06.94162+00	2026-09-18 17:59:06.945846+00
7da06823-77c7-4309-85dc-94f1657a7f98	b47a9352-a204-48da-88a2-4e0ff5199c9c	4b37e660-0d3a-4956-824d-0b2bc958b257	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 18:17:19.393768+00	2026-09-18 18:17:19.393768+00
e5941c54-c701-49c7-bbf6-3e88a342b672	83971df0-53aa-4f36-bf1f-4cdd946b55af	132cd1a1-8ddb-41ab-b21e-594ceee3abc1	796f5977-ae7c-418f-afa8-44f66724e640	revoked	2026-09-18 18:17:19.41171+00	2026-09-18 18:17:19.416422+00
23ed3ca4-41d9-4370-98b2-bd01df765b95	a1bfe903-9939-4ea1-ad12-045c85501004	989c770f-3092-4a04-8f92-0b6bd88cdc52	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 18:17:19.43863+00	2026-09-18 18:17:19.43863+00
0da87bdf-508f-4700-a3b3-709c51465ba5	a1bfe903-9939-4ea1-ad12-045c85501004	3fda60b2-2374-4fc0-a9d1-b343b25ddc75	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 18:17:19.4446+00	2026-09-18 18:17:19.4446+00
d7fc13d2-3c1e-4326-8312-fb881b7092cd	a1bfe903-9939-4ea1-ad12-045c85501004	a96c5d5d-ff89-4ab0-b528-852b0affd42a	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 18:17:19.44964+00	2026-09-18 18:17:19.44964+00
eed662c8-12a8-48c9-a9d2-196612752432	b8e89477-f324-4ec0-8b0d-0b8bb9075ab0	cf8f5724-8aac-4486-b32d-c6a0a2e16950	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 18:17:19.455682+00	2026-09-18 18:17:19.455682+00
c69a56cf-4162-41d8-9477-ba465f1993ed	b8e89477-f324-4ec0-8b0d-0b8bb9075ab0	2465f841-664a-4a2b-abf8-0b54656cf508	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 18:17:19.461687+00	2026-09-18 18:17:19.461687+00
a7fd55ee-1dee-4dba-9baa-f0f354ad155a	b8e89477-f324-4ec0-8b0d-0b8bb9075ab0	719e5b15-76f4-4d20-aaf1-dc114075ee43	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 18:17:19.467501+00	2026-09-18 18:17:19.467501+00
13886430-10e0-4b35-95a1-a69289087980	62c5c89d-7549-45f7-b04a-f19d9b6e7892	379443d0-65de-4362-b9b0-13fb44379f3d	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 18:17:19.472579+00	2026-09-18 18:17:19.472579+00
3cd8aa5b-322d-4e50-96d3-052412e934f2	62c5c89d-7549-45f7-b04a-f19d9b6e7892	92170218-9385-4174-8ed0-bf1cb5c83519	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 18:17:19.476648+00	2026-09-18 18:17:19.476648+00
2d2a4ba2-bafd-4c4f-8f15-14c08b70390e	62c5c89d-7549-45f7-b04a-f19d9b6e7892	fd2337f8-a595-472d-9f3f-211e06191e20	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 18:17:19.482507+00	2026-09-18 18:17:19.482507+00
180a9134-b49c-40df-9b5c-cb8a5f271474	1fa6367c-3247-4bef-8b0b-a98c33deab08	4a88cb87-5e81-4bc3-aa75-6826adbdf56a	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 18:17:19.497715+00	2026-09-18 18:17:19.497715+00
b077cfe7-4ab6-460a-9203-5b90325b778d	41a9dd2c-79c0-4043-8c2e-21eef4bbd4eb	2023ded3-5d8e-4605-9f49-809a602555b4	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 18:17:19.520596+00	2026-09-18 18:17:19.520596+00
b08453bb-bc58-4f30-912f-c713bdfd4dff	06b2520c-314b-40f8-a0cb-f83099826d28	41d58362-48d9-4bf9-ad61-251c7f4db173	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 18:17:29.556614+00	2026-09-18 18:17:29.556614+00
ae74dcb2-2134-4a86-a194-6cec416184a1	06b2520c-314b-40f8-a0cb-f83099826d28	8803a0db-9e98-4450-87ff-9b34cf2eeafd	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 18:17:29.564502+00	2026-09-18 18:17:29.564502+00
70bce97b-bac8-429c-8654-d2b5a66a8fa9	06b2520c-314b-40f8-a0cb-f83099826d28	ee526a4a-8fbb-42db-92af-480968e07c4d	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 18:17:29.569538+00	2026-09-18 18:17:29.569538+00
9c3c22ad-8849-431a-a524-7afba5b12e26	f98c4dae-d9a5-48d0-ad0d-a1c99a357371	b43bf662-d89a-46ba-bd26-23d378a8ae1f	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 18:17:29.574569+00	2026-09-18 18:17:29.574569+00
c83f6d64-4b44-4be2-8d19-8f12bb7c82ff	f98c4dae-d9a5-48d0-ad0d-a1c99a357371	8b1ed16d-47b1-4c70-8c82-0707b35631f1	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 18:17:29.579585+00	2026-09-18 18:17:29.579585+00
36645cd1-61d4-486d-aab4-6f30bc64acb0	f98c4dae-d9a5-48d0-ad0d-a1c99a357371	b4d80e2f-b403-49e8-86f9-f8c22112ea64	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 18:17:29.585624+00	2026-09-18 18:17:29.585624+00
2a8277ad-2a0e-4373-9e15-50c56919e19f	1853ec71-a724-4403-87f8-2857abbdae47	27b332ca-3cad-4abb-bad8-53cf0d217234	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 18:17:29.591451+00	2026-09-18 18:17:29.591451+00
4798fafd-5aa1-4e90-b5ce-634266b3fdb3	1853ec71-a724-4403-87f8-2857abbdae47	8cd019d8-bcd5-475e-ad66-7bbea0b253c2	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 18:17:29.595542+00	2026-09-18 18:17:29.595542+00
fa65c00c-2bd1-4887-9e62-54bc05c33f79	1853ec71-a724-4403-87f8-2857abbdae47	08495f8e-1396-4d0e-9f40-52882496379f	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 18:17:29.599592+00	2026-09-18 18:17:29.599592+00
507a392b-9d3a-4732-8605-938c797d27ab	42094e04-e1b6-4414-9010-03497a1ce1eb	1539079b-306c-4bc4-b198-68c7aafe9ab1	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 18:17:48.017294+00	2026-09-18 18:17:48.017294+00
9aafe9e0-8a0b-4b55-b12b-021bef7cd1aa	7c52f7a5-d6e3-426e-a882-cd8da87aa61e	31cc16da-8cba-42d3-ab2d-af3e4bec6159	796f5977-ae7c-418f-afa8-44f66724e640	revoked	2026-09-18 18:17:48.03429+00	2026-09-18 18:17:48.038901+00
79efa6fd-2926-4f49-b962-cfb42e2e1f9c	2ad0b90f-5109-4187-88a1-37ad3654d3c0	c4bca246-9c81-48e3-adab-2dab4b7e9b25	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 18:17:48.05917+00	2026-09-18 18:17:48.05917+00
0813d05a-d29b-4f7e-9a08-595c92e8f552	2ad0b90f-5109-4187-88a1-37ad3654d3c0	34040273-c48c-4b45-9b05-0ab4f627c4ef	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 18:17:48.065225+00	2026-09-18 18:17:48.065225+00
65687b05-5020-41cc-887e-a434def5d664	2ad0b90f-5109-4187-88a1-37ad3654d3c0	983c3d5e-af85-4ab0-af43-af04708104bb	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 18:17:48.07125+00	2026-09-18 18:17:48.07125+00
b0fd24d3-0935-48b9-b36f-6d44c9dc9af9	99886120-0baa-4d1e-a41c-25d434a84248	68db863b-d8c2-4acf-9efa-ef403172dc0e	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 18:17:48.07729+00	2026-09-18 18:17:48.07729+00
25cdd11a-ad42-4a32-ab2a-7e7d5ace3256	99886120-0baa-4d1e-a41c-25d434a84248	0d01748a-bc26-429a-aac8-b87562cd7805	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 18:17:48.083295+00	2026-09-18 18:17:48.083295+00
c281a366-57e2-4d3b-8acf-9c6c9c01d34f	99886120-0baa-4d1e-a41c-25d434a84248	ba6b9eb6-055d-4fa3-bfce-0611a929c3a3	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 18:17:48.089262+00	2026-09-18 18:17:48.089262+00
5f93f73d-5968-4d92-a4b8-3ac8df5c860b	585e682a-9213-4564-8c12-d48228b251fc	eaf46d98-1363-4929-a896-0392cffaf8f4	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 18:17:48.094288+00	2026-09-18 18:17:48.094288+00
89e3fb5e-3122-4880-a3b2-b598be23ad90	585e682a-9213-4564-8c12-d48228b251fc	5a27f090-2ebf-4589-89d7-27226ed2017c	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 18:17:48.099304+00	2026-09-18 18:17:48.099304+00
8099d1d2-c65b-4c3e-8ebd-906907affe08	585e682a-9213-4564-8c12-d48228b251fc	19e024ba-b461-4d45-9900-e146040d0fe7	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 18:17:48.104309+00	2026-09-18 18:17:48.104309+00
57c24f52-bfcc-445f-abd8-9aeca18acf15	79ac6a41-9080-48d3-ba9b-c9ae7e82a88c	5cb708a3-fbb9-48f7-8cb1-bad51f1acc10	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 18:17:48.121426+00	2026-09-18 18:17:48.121426+00
120a1d19-6c98-408f-861f-be08600c5a7f	bd18befd-3b4f-41ea-9047-d5e63bad87d9	c87651aa-eb63-4753-a7e0-a5cfe59f44b3	796f5977-ae7c-418f-afa8-44f66724e640	active	2026-09-18 18:17:48.145223+00	2026-09-18 18:17:48.145223+00
\.


--
-- Data for Name: outbox_events; Type: TABLE DATA; Schema: public; Owner: omnira
--

COPY public.outbox_events (id, tenant_id, event_type, aggregate_type, aggregate_id, correlation_id, causation_id, payload, occurred_at, published_at, attempts, created_at) FROM stdin;
\.


--
-- Data for Name: permissions; Type: TABLE DATA; Schema: public; Owner: omnira
--

COPY public.permissions (key, description) FROM stdin;
tenant.read	Read tenant information
tenant.manage	Manage tenant
membership.read	Read memberships
membership.manage	Manage memberships
audit.read	Read audit logs
hub.read	Read hub information
hub.manage	Manage hub
grant.read	Read hub-tenant grants
grant.manage	Manage hub-tenant grants
\.


--
-- Data for Name: role_permissions; Type: TABLE DATA; Schema: public; Owner: omnira
--

COPY public.role_permissions (role_id, permission_key) FROM stdin;
796f5977-ae7c-418f-afa8-44f66724e640	tenant.read
796f5977-ae7c-418f-afa8-44f66724e640	tenant.manage
796f5977-ae7c-418f-afa8-44f66724e640	membership.read
796f5977-ae7c-418f-afa8-44f66724e640	membership.manage
796f5977-ae7c-418f-afa8-44f66724e640	audit.read
1521370c-1a98-4fd9-8406-e1dce2675f47	tenant.read
1521370c-1a98-4fd9-8406-e1dce2675f47	membership.read
1521370c-1a98-4fd9-8406-e1dce2675f47	audit.read
45c043e5-2bbd-42a9-bfed-5addeb44ca83	tenant.read
\.


--
-- Data for Name: roles; Type: TABLE DATA; Schema: public; Owner: omnira
--

COPY public.roles (id, tenant_id, key, name, created_at) FROM stdin;
71cbb2da-b9e8-4a62-b530-3ab78512481e	\N	system_admin	System Administrator	2026-09-18 17:19:30.515639+00
796f5977-ae7c-418f-afa8-44f66724e640	\N	tenant_admin	Tenant Administrator	2026-09-18 17:19:30.515639+00
1521370c-1a98-4fd9-8406-e1dce2675f47	\N	tenant_supervisor	Tenant Supervisor	2026-09-18 17:19:30.515639+00
45c043e5-2bbd-42a9-bfed-5addeb44ca83	\N	tenant_agent	Tenant Agent	2026-09-18 17:19:30.515639+00
730f9946-56a1-4014-b41f-330cb37c73c1	\N	hub_admin	Hub Administrator	2026-09-18 17:19:30.515639+00
\.


--
-- Data for Name: tenants; Type: TABLE DATA; Schema: public; Owner: omnira
--

COPY public.tenants (id, legal_name, trade_name, tax_id, isolation_profile, status, created_at, updated_at) FROM stdin;
3c7326af-b963-4272-8a1e-668cdafadbca	Test Company	\N	\N	shared_strong_isolation	active	2026-09-18 17:57:23.547036+00	2026-09-18 17:57:23.547036+00
03d3078b-3b0a-430e-a1a9-0972fba22049	Test Company	\N	\N	shared_strong_isolation	active	2026-09-18 17:57:23.55782+00	2026-09-18 17:57:23.55782+00
f64d0814-4d60-41e0-84ea-8e57676638ce	Test Company	\N	\N	shared_strong_isolation	active	2026-09-18 17:58:07.989295+00	2026-09-18 17:58:07.989295+00
f10b8d64-d45b-44a7-afcf-1f0ba043939a	Test Company	\N	\N	shared_strong_isolation	active	2026-09-18 17:58:08.000811+00	2026-09-18 17:58:08.000811+00
f1b57252-17bb-4413-b461-9bd392a6941d	Test Company	\N	\N	shared_strong_isolation	active	2026-09-18 17:58:46.382739+00	2026-09-18 17:58:46.382742+00
54483015-cb8e-4c62-b2b9-b7ed539b8188	Test Company	\N	\N	shared_strong_isolation	active	2026-09-18 17:58:46.394939+00	2026-09-18 17:58:46.394939+00
8ab3e071-d031-4d18-b0ab-9bddefed9c27	Test Company	\N	\N	shared_strong_isolation	active	2026-09-18 17:58:59.946103+00	2026-09-18 17:58:59.946103+00
a8a70035-805b-4add-972d-144778c5ae17	Test Company	\N	\N	shared_strong_isolation	active	2026-09-18 17:58:59.958067+00	2026-09-18 17:58:59.958067+00
d9d57d2d-e1da-4381-824f-d9d93138cc8f	Test Company	\N	\N	shared_strong_isolation	active	2026-09-18 17:59:03.291391+00	2026-09-18 17:59:03.291391+00
91c57e4f-a7aa-4b73-8899-c4552110ec0b	Test Company	\N	\N	shared_strong_isolation	active	2026-09-18 17:59:03.301505+00	2026-09-18 17:59:03.301505+00
c9a96f9b-c9ab-4c30-9e91-6def6295a9ff	Tenant A	\N	\N	shared_strong_isolation	active	2026-09-18 17:59:03.319587+00	2026-09-18 17:59:03.319587+00
766ccf75-6737-4beb-b04d-f04e174b331c	Tenant B	\N	\N	shared_strong_isolation	active	2026-09-18 17:59:03.323951+00	2026-09-18 17:59:03.323951+00
706eb126-d928-4196-993e-1dfe58c8050f	Tenant A	\N	\N	shared_strong_isolation	active	2026-09-18 17:59:03.34037+00	2026-09-18 17:59:03.34037+00
8ad2be27-a2ec-4f06-a1eb-89c7c3978fae	Tenant 1	\N	\N	shared_strong_isolation	active	2026-09-18 17:59:03.362195+00	2026-09-18 17:59:03.362195+00
ebab7ce3-02c7-40d2-ba3c-fb42da7a48b2	Tenant 2	\N	\N	shared_strong_isolation	active	2026-09-18 17:59:03.366002+00	2026-09-18 17:59:03.366002+00
a38cf23f-c783-4bc5-8bb9-85df519a5cc9	Tenant 3	\N	\N	shared_strong_isolation	active	2026-09-18 17:59:03.369022+00	2026-09-18 17:59:03.369022+00
9ea0a3f7-0c7f-4567-9408-c436ad3eedc4	Test Company	\N	\N	shared_strong_isolation	active	2026-09-18 17:59:06.886122+00	2026-09-18 17:59:06.886122+00
6d3efbe8-4575-473e-824b-da5257d04478	Test Company	\N	\N	shared_strong_isolation	active	2026-09-18 17:59:06.895797+00	2026-09-18 17:59:06.895797+00
1b4b0b07-290e-4a77-ab0f-d91fa5e2fafb	Tenant A	\N	\N	shared_strong_isolation	active	2026-09-18 17:59:06.913505+00	2026-09-18 17:59:06.913505+00
e562518f-f866-42a2-a972-28e184062b64	Tenant B	\N	\N	shared_strong_isolation	active	2026-09-18 17:59:06.917347+00	2026-09-18 17:59:06.917347+00
2c717d4b-2305-4cb1-8794-5c0911443b2d	Tenant A	\N	\N	shared_strong_isolation	active	2026-09-18 17:59:06.933238+00	2026-09-18 17:59:06.933238+00
16f0d7de-57ce-48ed-b7b5-0f77565773d9	Tenant 1	\N	\N	shared_strong_isolation	active	2026-09-18 17:59:06.953011+00	2026-09-18 17:59:06.953011+00
ce0d3e3e-9fc1-47cb-8d7e-e9a6994e2a23	Tenant 2	\N	\N	shared_strong_isolation	active	2026-09-18 17:59:06.957326+00	2026-09-18 17:59:06.957327+00
7d6357ce-e90b-4cd9-bd9a-dff59786b878	Tenant 3	\N	\N	shared_strong_isolation	active	2026-09-18 17:59:06.960341+00	2026-09-18 17:59:06.960341+00
b47a9352-a204-48da-88a2-4e0ff5199c9c	Tenant A Corp	\N	\N	shared_strong_isolation	active	2026-09-18 18:17:19.378969+00	2026-09-18 18:17:19.378969+00
cf3e6f68-bf80-4191-8ebb-b8d05886e8e6	Tenant B Corp	\N	\N	shared_strong_isolation	active	2026-09-18 18:17:19.386595+00	2026-09-18 18:17:19.386595+00
83971df0-53aa-4f36-bf1f-4cdd946b55af	Test Tenant	\N	\N	shared_strong_isolation	active	2026-09-18 18:17:19.403076+00	2026-09-18 18:17:19.403076+00
a1bfe903-9939-4ea1-ad12-045c85501004	Tenant A	\N	\N	shared_strong_isolation	active	2026-09-18 18:17:19.424411+00	2026-09-18 18:17:19.424411+00
b8e89477-f324-4ec0-8b0d-0b8bb9075ab0	Tenant B	\N	\N	shared_strong_isolation	active	2026-09-18 18:17:19.428653+00	2026-09-18 18:17:19.428653+00
62c5c89d-7549-45f7-b04a-f19d9b6e7892	Tenant C	\N	\N	shared_strong_isolation	active	2026-09-18 18:17:19.430613+00	2026-09-18 18:17:19.430613+00
1fa6367c-3247-4bef-8b0b-a98c33deab08	Test Tenant	\N	\N	shared_strong_isolation	inactive	2026-09-18 18:17:19.489702+00	2026-09-18 18:17:19.502357+00
41a9dd2c-79c0-4043-8c2e-21eef4bbd4eb	Tenant A	\N	\N	shared_strong_isolation	active	2026-09-18 18:17:19.510182+00	2026-09-18 18:17:19.510182+00
2ae172f8-6b36-42d8-b8bd-5d6b286217e5	Tenant B	\N	\N	shared_strong_isolation	active	2026-09-18 18:17:19.514598+00	2026-09-18 18:17:19.514598+00
8da88118-bb0e-45bf-b97a-f4e7bbaff39d	Tenant 1	\N	\N	shared_strong_isolation	active	2026-09-18 18:17:19.529814+00	2026-09-18 18:17:19.529814+00
99186793-670e-42ff-a9d6-3c4986321ef7	Tenant 2	\N	\N	shared_strong_isolation	active	2026-09-18 18:17:19.533546+00	2026-09-18 18:17:19.533546+00
d0b4cf43-f013-42b1-a552-5c66983403d0	Tenant 3	\N	\N	shared_strong_isolation	active	2026-09-18 18:17:19.535578+00	2026-09-18 18:17:19.535578+00
06b2520c-314b-40f8-a0cb-f83099826d28	Tenant A	\N	\N	shared_strong_isolation	active	2026-09-18 18:17:29.532626+00	2026-09-18 18:17:29.532627+00
f98c4dae-d9a5-48d0-ad0d-a1c99a357371	Tenant B	\N	\N	shared_strong_isolation	active	2026-09-18 18:17:29.539513+00	2026-09-18 18:17:29.539513+00
1853ec71-a724-4403-87f8-2857abbdae47	Tenant C	\N	\N	shared_strong_isolation	active	2026-09-18 18:17:29.544531+00	2026-09-18 18:17:29.544531+00
db91c671-dd2b-4e71-aba8-41bf7797c4c9	Tenant 1	\N	\N	shared_strong_isolation	active	2026-09-18 18:17:29.60784+00	2026-09-18 18:17:29.60784+00
e30d5ae6-06a5-4d84-8957-918e30f170bf	Tenant 2	\N	\N	shared_strong_isolation	active	2026-09-18 18:17:29.61156+00	2026-09-18 18:17:29.61156+00
ca88ee6e-0b4a-4eac-9f6a-7d7631a83632	Tenant 3	\N	\N	shared_strong_isolation	active	2026-09-18 18:17:29.61358+00	2026-09-18 18:17:29.61358+00
42094e04-e1b6-4414-9010-03497a1ce1eb	Tenant A Corp	\N	\N	shared_strong_isolation	active	2026-09-18 18:17:48.005862+00	2026-09-18 18:17:48.005862+00
6367e222-682c-4098-bdd1-3e72ab8657cb	Tenant B Corp	\N	\N	shared_strong_isolation	active	2026-09-18 18:17:48.010349+00	2026-09-18 18:17:48.010349+00
7c52f7a5-d6e3-426e-a882-cd8da87aa61e	Test Tenant	\N	\N	shared_strong_isolation	active	2026-09-18 18:17:48.026576+00	2026-09-18 18:17:48.026576+00
2ad0b90f-5109-4187-88a1-37ad3654d3c0	Tenant A	\N	\N	shared_strong_isolation	active	2026-09-18 18:17:48.045819+00	2026-09-18 18:17:48.045819+00
99886120-0baa-4d1e-a41c-25d434a84248	Tenant B	\N	\N	shared_strong_isolation	active	2026-09-18 18:17:48.05016+00	2026-09-18 18:17:48.05016+00
585e682a-9213-4564-8c12-d48228b251fc	Tenant C	\N	\N	shared_strong_isolation	active	2026-09-18 18:17:48.05317+00	2026-09-18 18:17:48.05317+00
79ac6a41-9080-48d3-ba9b-c9ae7e82a88c	Test Tenant	\N	\N	shared_strong_isolation	inactive	2026-09-18 18:17:48.112789+00	2026-09-18 18:17:48.126108+00
bd18befd-3b4f-41ea-9047-d5e63bad87d9	Tenant A	\N	\N	shared_strong_isolation	active	2026-09-18 18:17:48.133749+00	2026-09-18 18:17:48.133749+00
8eeabeaf-4dd5-43d5-a76c-e49e3ecf5304	Tenant B	\N	\N	shared_strong_isolation	active	2026-09-18 18:17:48.138305+00	2026-09-18 18:17:48.138305+00
\.


--
-- Data for Name: users; Type: TABLE DATA; Schema: public; Owner: omnira
--

COPY public.users (id, external_subject, email, display_name, status, created_at, updated_at) FROM stdin;
6f60bebe-a2dd-43de-9d82-52752411a214	test-user	test@example.com	\N	active	2026-09-18 17:58:08.005169+00	2026-09-18 17:58:08.005169+00
585f7f12-14c6-4d8b-9b6d-53ae571b5493	585f7f12-14c6-4d8b-9b6d-53ae571b5493	585f7f12-14c6-4d8b-9b6d-53ae571b5493@example.com	\N	active	2026-09-18 17:58:59.962159+00	2026-09-18 17:58:59.962159+00
259a4c79-d79e-41f1-881c-dc66093c93b7	259a4c79-d79e-41f1-881c-dc66093c93b7	259a4c79-d79e-41f1-881c-dc66093c93b7@example.com	\N	active	2026-09-18 17:59:03.306309+00	2026-09-18 17:59:03.306309+00
8dacffce-da9d-4960-95fd-34da055a166c	8dacffce-da9d-4960-95fd-34da055a166c	8dacffce-da9d-4960-95fd-34da055a166c@example.com	\N	active	2026-09-18 17:59:03.327404+00	2026-09-18 17:59:03.327404+00
0f22be05-6d45-4e6f-84d1-671a59556984	0f22be05-6d45-4e6f-84d1-671a59556984	0f22be05-6d45-4e6f-84d1-671a59556984@example.com	\N	active	2026-09-18 17:59:03.345264+00	2026-09-18 17:59:03.345264+00
e40d6474-508d-43e1-bba5-fd8164944eb9	user-t1-u1	user-t1-u1@example.com	\N	active	2026-09-18 17:59:03.372027+00	2026-09-18 17:59:03.372027+00
db666020-7b24-4316-9806-b5f06b92eed9	user-t1-u2	user-t1-u2@example.com	\N	active	2026-09-18 17:59:03.379441+00	2026-09-18 17:59:03.379441+00
be48471f-4a86-4b2a-9f60-da9bc9d8d85b	user-t2-u1	user-t2-u1@example.com	\N	active	2026-09-18 17:59:03.385183+00	2026-09-18 17:59:03.385183+00
62facbcb-3d19-480e-811a-b2965c4a354f	user-t2-u2	user-t2-u2@example.com	\N	active	2026-09-18 17:59:03.391355+00	2026-09-18 17:59:03.391355+00
d4036bd5-e571-4525-8e5d-3e9d8466ac9d	user-t3-u1	user-t3-u1@example.com	\N	active	2026-09-18 17:59:03.39725+00	2026-09-18 17:59:03.39725+00
0f20daa4-44fd-4184-8b97-52efe33e1b06	user-t3-u2	user-t3-u2@example.com	\N	active	2026-09-18 17:59:03.403178+00	2026-09-18 17:59:03.403178+00
4fbf70bf-5ced-43d3-81f9-23197cb15c7e	4fbf70bf-5ced-43d3-81f9-23197cb15c7e	4fbf70bf-5ced-43d3-81f9-23197cb15c7e@example.com	\N	active	2026-09-18 17:59:06.90074+00	2026-09-18 17:59:06.90074+00
5a58f1f9-37d2-4774-a388-e8d1a41f76c5	5a58f1f9-37d2-4774-a388-e8d1a41f76c5	5a58f1f9-37d2-4774-a388-e8d1a41f76c5@example.com	\N	active	2026-09-18 17:59:06.920709+00	2026-09-18 17:59:06.920709+00
da4256db-5246-490e-95bb-7994cd01324d	da4256db-5246-490e-95bb-7994cd01324d	da4256db-5246-490e-95bb-7994cd01324d@example.com	\N	active	2026-09-18 17:59:06.937726+00	2026-09-18 17:59:06.937726+00
4b37e660-0d3a-4956-824d-0b2bc958b257	4b37e660-0d3a-4956-824d-0b2bc958b257	4b37e660-0d3a-4956-824d-0b2bc958b257@example.com	\N	active	2026-09-18 18:17:19.389972+00	2026-09-18 18:17:19.389972+00
132cd1a1-8ddb-41ab-b21e-594ceee3abc1	132cd1a1-8ddb-41ab-b21e-594ceee3abc1	132cd1a1-8ddb-41ab-b21e-594ceee3abc1@example.com	\N	active	2026-09-18 18:17:19.407869+00	2026-09-18 18:17:19.407869+00
989c770f-3092-4a04-8f92-0b6bd88cdc52	989c770f-3092-4a04-8f92-0b6bd88cdc52	989c770f-3092-4a04-8f92-0b6bd88cdc52@example.com	\N	active	2026-09-18 18:17:19.435048+00	2026-09-18 18:17:19.435048+00
3fda60b2-2374-4fc0-a9d1-b343b25ddc75	3fda60b2-2374-4fc0-a9d1-b343b25ddc75	3fda60b2-2374-4fc0-a9d1-b343b25ddc75@example.com	\N	active	2026-09-18 18:17:19.442622+00	2026-09-18 18:17:19.442622+00
a96c5d5d-ff89-4ab0-b528-852b0affd42a	a96c5d5d-ff89-4ab0-b528-852b0affd42a	a96c5d5d-ff89-4ab0-b528-852b0affd42a@example.com	\N	active	2026-09-18 18:17:19.447682+00	2026-09-18 18:17:19.447682+00
cf8f5724-8aac-4486-b32d-c6a0a2e16950	cf8f5724-8aac-4486-b32d-c6a0a2e16950	cf8f5724-8aac-4486-b32d-c6a0a2e16950@example.com	\N	active	2026-09-18 18:17:19.452688+00	2026-09-18 18:17:19.452688+00
2465f841-664a-4a2b-abf8-0b54656cf508	2465f841-664a-4a2b-abf8-0b54656cf508	2465f841-664a-4a2b-abf8-0b54656cf508@example.com	\N	active	2026-09-18 18:17:19.458714+00	2026-09-18 18:17:19.458714+00
719e5b15-76f4-4d20-aaf1-dc114075ee43	719e5b15-76f4-4d20-aaf1-dc114075ee43	719e5b15-76f4-4d20-aaf1-dc114075ee43@example.com	\N	active	2026-09-18 18:17:19.464722+00	2026-09-18 18:17:19.464722+00
379443d0-65de-4362-b9b0-13fb44379f3d	379443d0-65de-4362-b9b0-13fb44379f3d	379443d0-65de-4362-b9b0-13fb44379f3d@example.com	\N	active	2026-09-18 18:17:19.470578+00	2026-09-18 18:17:19.470578+00
92170218-9385-4174-8ed0-bf1cb5c83519	92170218-9385-4174-8ed0-bf1cb5c83519	92170218-9385-4174-8ed0-bf1cb5c83519@example.com	\N	active	2026-09-18 18:17:19.474657+00	2026-09-18 18:17:19.474657+00
fd2337f8-a595-472d-9f3f-211e06191e20	fd2337f8-a595-472d-9f3f-211e06191e20	fd2337f8-a595-472d-9f3f-211e06191e20@example.com	\N	active	2026-09-18 18:17:19.47972+00	2026-09-18 18:17:19.47972+00
4a88cb87-5e81-4bc3-aa75-6826adbdf56a	4a88cb87-5e81-4bc3-aa75-6826adbdf56a	4a88cb87-5e81-4bc3-aa75-6826adbdf56a@example.com	\N	active	2026-09-18 18:17:19.493902+00	2026-09-18 18:17:19.493902+00
2023ded3-5d8e-4605-9f49-809a602555b4	2023ded3-5d8e-4605-9f49-809a602555b4	2023ded3-5d8e-4605-9f49-809a602555b4@example.com	\N	active	2026-09-18 18:17:19.51696+00	2026-09-18 18:17:19.51696+00
41d58362-48d9-4bf9-ad61-251c7f4db173	41d58362-48d9-4bf9-ad61-251c7f4db173	41d58362-48d9-4bf9-ad61-251c7f4db173@example.com	\N	active	2026-09-18 18:17:29.551094+00	2026-09-18 18:17:29.551094+00
8803a0db-9e98-4450-87ff-9b34cf2eeafd	8803a0db-9e98-4450-87ff-9b34cf2eeafd	8803a0db-9e98-4450-87ff-9b34cf2eeafd@example.com	\N	active	2026-09-18 18:17:29.562036+00	2026-09-18 18:17:29.562036+00
ee526a4a-8fbb-42db-92af-480968e07c4d	ee526a4a-8fbb-42db-92af-480968e07c4d	ee526a4a-8fbb-42db-92af-480968e07c4d@example.com	\N	active	2026-09-18 18:17:29.567557+00	2026-09-18 18:17:29.567557+00
b43bf662-d89a-46ba-bd26-23d378a8ae1f	b43bf662-d89a-46ba-bd26-23d378a8ae1f	b43bf662-d89a-46ba-bd26-23d378a8ae1f@example.com	\N	active	2026-09-18 18:17:29.572591+00	2026-09-18 18:17:29.572591+00
8b1ed16d-47b1-4c70-8c82-0707b35631f1	8b1ed16d-47b1-4c70-8c82-0707b35631f1	8b1ed16d-47b1-4c70-8c82-0707b35631f1@example.com	\N	active	2026-09-18 18:17:29.577614+00	2026-09-18 18:17:29.577614+00
b4d80e2f-b403-49e8-86f9-f8c22112ea64	b4d80e2f-b403-49e8-86f9-f8c22112ea64	b4d80e2f-b403-49e8-86f9-f8c22112ea64@example.com	\N	active	2026-09-18 18:17:29.582633+00	2026-09-18 18:17:29.582633+00
27b332ca-3cad-4abb-bad8-53cf0d217234	27b332ca-3cad-4abb-bad8-53cf0d217234	27b332ca-3cad-4abb-bad8-53cf0d217234@example.com	\N	active	2026-09-18 18:17:29.58866+00	2026-09-18 18:17:29.58866+00
8cd019d8-bcd5-475e-ad66-7bbea0b253c2	8cd019d8-bcd5-475e-ad66-7bbea0b253c2	8cd019d8-bcd5-475e-ad66-7bbea0b253c2@example.com	\N	active	2026-09-18 18:17:29.59354+00	2026-09-18 18:17:29.59354+00
08495f8e-1396-4d0e-9f40-52882496379f	08495f8e-1396-4d0e-9f40-52882496379f	08495f8e-1396-4d0e-9f40-52882496379f@example.com	\N	active	2026-09-18 18:17:29.597623+00	2026-09-18 18:17:29.597623+00
1539079b-306c-4bc4-b198-68c7aafe9ab1	1539079b-306c-4bc4-b198-68c7aafe9ab1	1539079b-306c-4bc4-b198-68c7aafe9ab1@example.com	\N	active	2026-09-18 18:17:48.013655+00	2026-09-18 18:17:48.013655+00
31cc16da-8cba-42d3-ab2d-af3e4bec6159	31cc16da-8cba-42d3-ab2d-af3e4bec6159	31cc16da-8cba-42d3-ab2d-af3e4bec6159@example.com	\N	active	2026-09-18 18:17:48.030659+00	2026-09-18 18:17:48.030659+00
c4bca246-9c81-48e3-adab-2dab4b7e9b25	c4bca246-9c81-48e3-adab-2dab4b7e9b25	c4bca246-9c81-48e3-adab-2dab4b7e9b25@example.com	\N	active	2026-09-18 18:17:48.056596+00	2026-09-18 18:17:48.056596+00
34040273-c48c-4b45-9b05-0ab4f627c4ef	34040273-c48c-4b45-9b05-0ab4f627c4ef	34040273-c48c-4b45-9b05-0ab4f627c4ef@example.com	\N	active	2026-09-18 18:17:48.063224+00	2026-09-18 18:17:48.063224+00
983c3d5e-af85-4ab0-af43-af04708104bb	983c3d5e-af85-4ab0-af43-af04708104bb	983c3d5e-af85-4ab0-af43-af04708104bb@example.com	\N	active	2026-09-18 18:17:48.068297+00	2026-09-18 18:17:48.068297+00
68db863b-d8c2-4acf-9efa-ef403172dc0e	68db863b-d8c2-4acf-9efa-ef403172dc0e	68db863b-d8c2-4acf-9efa-ef403172dc0e@example.com	\N	active	2026-09-18 18:17:48.074314+00	2026-09-18 18:17:48.074314+00
0d01748a-bc26-429a-aac8-b87562cd7805	0d01748a-bc26-429a-aac8-b87562cd7805	0d01748a-bc26-429a-aac8-b87562cd7805@example.com	\N	active	2026-09-18 18:17:48.080438+00	2026-09-18 18:17:48.080438+00
ba6b9eb6-055d-4fa3-bfce-0611a929c3a3	ba6b9eb6-055d-4fa3-bfce-0611a929c3a3	ba6b9eb6-055d-4fa3-bfce-0611a929c3a3@example.com	\N	active	2026-09-18 18:17:48.086306+00	2026-09-18 18:17:48.086306+00
eaf46d98-1363-4929-a896-0392cffaf8f4	eaf46d98-1363-4929-a896-0392cffaf8f4	eaf46d98-1363-4929-a896-0392cffaf8f4@example.com	\N	active	2026-09-18 18:17:48.09231+00	2026-09-18 18:17:48.09231+00
5a27f090-2ebf-4589-89d7-27226ed2017c	5a27f090-2ebf-4589-89d7-27226ed2017c	5a27f090-2ebf-4589-89d7-27226ed2017c@example.com	\N	active	2026-09-18 18:17:48.097332+00	2026-09-18 18:17:48.097332+00
19e024ba-b461-4d45-9900-e146040d0fe7	19e024ba-b461-4d45-9900-e146040d0fe7	19e024ba-b461-4d45-9900-e146040d0fe7@example.com	\N	active	2026-09-18 18:17:48.102354+00	2026-09-18 18:17:48.102354+00
5cb708a3-fbb9-48f7-8cb1-bad51f1acc10	5cb708a3-fbb9-48f7-8cb1-bad51f1acc10	5cb708a3-fbb9-48f7-8cb1-bad51f1acc10@example.com	\N	active	2026-09-18 18:17:48.117642+00	2026-09-18 18:17:48.117642+00
c87651aa-eb63-4753-a7e0-a5cfe59f44b3	c87651aa-eb63-4753-a7e0-a5cfe59f44b3	c87651aa-eb63-4753-a7e0-a5cfe59f44b3@example.com	\N	active	2026-09-18 18:17:48.141625+00	2026-09-18 18:17:48.141625+00
\.


--
-- Name: audit_events audit_events_pkey; Type: CONSTRAINT; Schema: public; Owner: omnira
--

ALTER TABLE ONLY public.audit_events
    ADD CONSTRAINT audit_events_pkey PRIMARY KEY (id);


--
-- Name: memberships memberships_pkey; Type: CONSTRAINT; Schema: public; Owner: omnira
--

ALTER TABLE ONLY public.memberships
    ADD CONSTRAINT memberships_pkey PRIMARY KEY (id);


--
-- Name: memberships memberships_tenant_id_user_id_key; Type: CONSTRAINT; Schema: public; Owner: omnira
--

ALTER TABLE ONLY public.memberships
    ADD CONSTRAINT memberships_tenant_id_user_id_key UNIQUE (tenant_id, user_id);


--
-- Name: outbox_events outbox_events_pkey; Type: CONSTRAINT; Schema: public; Owner: omnira
--

ALTER TABLE ONLY public.outbox_events
    ADD CONSTRAINT outbox_events_pkey PRIMARY KEY (id);


--
-- Name: permissions permissions_pkey; Type: CONSTRAINT; Schema: public; Owner: omnira
--

ALTER TABLE ONLY public.permissions
    ADD CONSTRAINT permissions_pkey PRIMARY KEY (key);


--
-- Name: role_permissions role_permissions_pkey; Type: CONSTRAINT; Schema: public; Owner: omnira
--

ALTER TABLE ONLY public.role_permissions
    ADD CONSTRAINT role_permissions_pkey PRIMARY KEY (role_id, permission_key);


--
-- Name: roles roles_pkey; Type: CONSTRAINT; Schema: public; Owner: omnira
--

ALTER TABLE ONLY public.roles
    ADD CONSTRAINT roles_pkey PRIMARY KEY (id);


--
-- Name: roles roles_tenant_id_key_key; Type: CONSTRAINT; Schema: public; Owner: omnira
--

ALTER TABLE ONLY public.roles
    ADD CONSTRAINT roles_tenant_id_key_key UNIQUE (tenant_id, key);


--
-- Name: tenants tenants_pkey; Type: CONSTRAINT; Schema: public; Owner: omnira
--

ALTER TABLE ONLY public.tenants
    ADD CONSTRAINT tenants_pkey PRIMARY KEY (id);


--
-- Name: users users_external_subject_key; Type: CONSTRAINT; Schema: public; Owner: omnira
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_external_subject_key UNIQUE (external_subject);


--
-- Name: users users_pkey; Type: CONSTRAINT; Schema: public; Owner: omnira
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_pkey PRIMARY KEY (id);


--
-- Name: idx_audit_events_actor_id; Type: INDEX; Schema: public; Owner: omnira
--

CREATE INDEX idx_audit_events_actor_id ON public.audit_events USING btree (actor_id);


--
-- Name: idx_audit_events_correlation_id; Type: INDEX; Schema: public; Owner: omnira
--

CREATE INDEX idx_audit_events_correlation_id ON public.audit_events USING btree (correlation_id);


--
-- Name: idx_audit_events_created_at; Type: INDEX; Schema: public; Owner: omnira
--

CREATE INDEX idx_audit_events_created_at ON public.audit_events USING btree (created_at DESC);


--
-- Name: idx_audit_events_resource; Type: INDEX; Schema: public; Owner: omnira
--

CREATE INDEX idx_audit_events_resource ON public.audit_events USING btree (resource_type, resource_id);


--
-- Name: idx_audit_events_tenant_id; Type: INDEX; Schema: public; Owner: omnira
--

CREATE INDEX idx_audit_events_tenant_id ON public.audit_events USING btree (tenant_id);


--
-- Name: idx_memberships_role_id; Type: INDEX; Schema: public; Owner: omnira
--

CREATE INDEX idx_memberships_role_id ON public.memberships USING btree (role_id);


--
-- Name: idx_memberships_status; Type: INDEX; Schema: public; Owner: omnira
--

CREATE INDEX idx_memberships_status ON public.memberships USING btree (status);


--
-- Name: idx_memberships_tenant_id; Type: INDEX; Schema: public; Owner: omnira
--

CREATE INDEX idx_memberships_tenant_id ON public.memberships USING btree (tenant_id);


--
-- Name: idx_memberships_user_id; Type: INDEX; Schema: public; Owner: omnira
--

CREATE INDEX idx_memberships_user_id ON public.memberships USING btree (user_id);


--
-- Name: idx_outbox_events_created_at; Type: INDEX; Schema: public; Owner: omnira
--

CREATE INDEX idx_outbox_events_created_at ON public.outbox_events USING btree (created_at);


--
-- Name: idx_outbox_events_event_type; Type: INDEX; Schema: public; Owner: omnira
--

CREATE INDEX idx_outbox_events_event_type ON public.outbox_events USING btree (event_type);


--
-- Name: idx_outbox_events_published_at; Type: INDEX; Schema: public; Owner: omnira
--

CREATE INDEX idx_outbox_events_published_at ON public.outbox_events USING btree (published_at) WHERE (published_at IS NULL);


--
-- Name: idx_outbox_events_tenant_id; Type: INDEX; Schema: public; Owner: omnira
--

CREATE INDEX idx_outbox_events_tenant_id ON public.outbox_events USING btree (tenant_id);


--
-- Name: idx_roles_key; Type: INDEX; Schema: public; Owner: omnira
--

CREATE INDEX idx_roles_key ON public.roles USING btree (key);


--
-- Name: idx_roles_tenant_id; Type: INDEX; Schema: public; Owner: omnira
--

CREATE INDEX idx_roles_tenant_id ON public.roles USING btree (tenant_id);


--
-- Name: idx_tenants_created_at; Type: INDEX; Schema: public; Owner: omnira
--

CREATE INDEX idx_tenants_created_at ON public.tenants USING btree (created_at DESC);


--
-- Name: idx_tenants_status; Type: INDEX; Schema: public; Owner: omnira
--

CREATE INDEX idx_tenants_status ON public.tenants USING btree (status);


--
-- Name: idx_users_external_subject; Type: INDEX; Schema: public; Owner: omnira
--

CREATE INDEX idx_users_external_subject ON public.users USING btree (external_subject);


--
-- Name: audit_events audit_events_actor_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: omnira
--

ALTER TABLE ONLY public.audit_events
    ADD CONSTRAINT audit_events_actor_id_fkey FOREIGN KEY (actor_id) REFERENCES public.users(id) ON DELETE SET NULL;


--
-- Name: audit_events audit_events_tenant_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: omnira
--

ALTER TABLE ONLY public.audit_events
    ADD CONSTRAINT audit_events_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE CASCADE;


--
-- Name: memberships memberships_role_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: omnira
--

ALTER TABLE ONLY public.memberships
    ADD CONSTRAINT memberships_role_id_fkey FOREIGN KEY (role_id) REFERENCES public.roles(id) ON DELETE RESTRICT;


--
-- Name: memberships memberships_tenant_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: omnira
--

ALTER TABLE ONLY public.memberships
    ADD CONSTRAINT memberships_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE CASCADE;


--
-- Name: memberships memberships_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: omnira
--

ALTER TABLE ONLY public.memberships
    ADD CONSTRAINT memberships_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: outbox_events outbox_events_tenant_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: omnira
--

ALTER TABLE ONLY public.outbox_events
    ADD CONSTRAINT outbox_events_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE CASCADE;


--
-- Name: role_permissions role_permissions_permission_key_fkey; Type: FK CONSTRAINT; Schema: public; Owner: omnira
--

ALTER TABLE ONLY public.role_permissions
    ADD CONSTRAINT role_permissions_permission_key_fkey FOREIGN KEY (permission_key) REFERENCES public.permissions(key) ON DELETE CASCADE;


--
-- Name: role_permissions role_permissions_role_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: omnira
--

ALTER TABLE ONLY public.role_permissions
    ADD CONSTRAINT role_permissions_role_id_fkey FOREIGN KEY (role_id) REFERENCES public.roles(id) ON DELETE CASCADE;


--
-- Name: roles roles_tenant_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: omnira
--

ALTER TABLE ONLY public.roles
    ADD CONSTRAINT roles_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE CASCADE;


--
-- Name: audit_events; Type: ROW SECURITY; Schema: public; Owner: omnira
--

ALTER TABLE public.audit_events ENABLE ROW LEVEL SECURITY;

--
-- Name: memberships; Type: ROW SECURITY; Schema: public; Owner: omnira
--

ALTER TABLE public.memberships ENABLE ROW LEVEL SECURITY;

--
-- Name: outbox_events; Type: ROW SECURITY; Schema: public; Owner: omnira
--

ALTER TABLE public.outbox_events ENABLE ROW LEVEL SECURITY;

--
-- Name: permissions; Type: ROW SECURITY; Schema: public; Owner: omnira
--

ALTER TABLE public.permissions ENABLE ROW LEVEL SECURITY;

--
-- Name: permissions permissions_read_public; Type: POLICY; Schema: public; Owner: omnira
--

CREATE POLICY permissions_read_public ON public.permissions FOR SELECT USING (true);


--
-- Name: role_permissions; Type: ROW SECURITY; Schema: public; Owner: omnira
--

ALTER TABLE public.role_permissions ENABLE ROW LEVEL SECURITY;

--
-- Name: role_permissions role_permissions_read_public; Type: POLICY; Schema: public; Owner: omnira
--

CREATE POLICY role_permissions_read_public ON public.role_permissions FOR SELECT USING (true);


--
-- Name: roles; Type: ROW SECURITY; Schema: public; Owner: omnira
--

ALTER TABLE public.roles ENABLE ROW LEVEL SECURITY;

--
-- Name: roles roles_read_public; Type: POLICY; Schema: public; Owner: omnira
--

CREATE POLICY roles_read_public ON public.roles FOR SELECT USING (true);


--
-- Name: tenants; Type: ROW SECURITY; Schema: public; Owner: omnira
--

ALTER TABLE public.tenants ENABLE ROW LEVEL SECURITY;

--
-- Name: users; Type: ROW SECURITY; Schema: public; Owner: omnira
--

ALTER TABLE public.users ENABLE ROW LEVEL SECURITY;

--
-- PostgreSQL database dump complete
--

\unrestrict OcycDndAtiFfv4II1rfClnQVkJ4Exy2Qv9qMfGAtpAHbwqDbH8qRPKAwYXUl4Xu


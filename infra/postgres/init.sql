-- Local development identities only. The runtime role cannot own schema objects.
CREATE ROLE jungle_app LOGIN PASSWORD 'local-app-only' NOSUPERUSER NOCREATEDB NOCREATEROLE;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
GRANT CONNECT ON DATABASE jungle TO jungle_app;
GRANT USAGE ON SCHEMA public TO jungle_app;

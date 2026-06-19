-- Best-effort teardown of the baseline schema (dev/reset only). FK-dependent
-- tables first; CASCADE covers the rest.
DROP TABLE IF EXISTS notebook_kernels;
DROP TABLE IF EXISTS user_kernel_pods;
DROP TABLE IF EXISTS app_secrets;
DROP TABLE IF EXISTS connectors;
DROP TABLE IF EXISTS allowed_email_rules;
DROP TABLE IF EXISTS notebook_cells;
DROP TABLE IF EXISTS notebooks;
DROP TABLE IF EXISTS admins;

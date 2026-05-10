CREATE TABLE projects (
  id         VARCHAR(64)  NOT NULL PRIMARY KEY,
  name       VARCHAR(255) NOT NULL,
  owner_id   VARCHAR(64)  NOT NULL,
  created_at DATETIME(6)  NOT NULL DEFAULT NOW(6)
);

CREATE TABLE devices (
  id         VARCHAR(64)  NOT NULL PRIMARY KEY,
  project_id VARCHAR(64)  NOT NULL,
  name       VARCHAR(255) NOT NULL,
  created_at DATETIME(6)  NOT NULL DEFAULT NOW(6),
  FOREIGN KEY (project_id) REFERENCES projects(id)
);

terraform {
  required_providers {
    coder = {
      source  = "coder/coder"
      version = "0.6.14"
    }
    docker = {
      source  = "kreuzwerker/docker"
      version = "~> 2.22.0"
    }
  }
}

locals {
  // These are Tailscale IP addresses. Ask Dean or Kyle for help.
  docker_host = {
    ""              = "tcp://100.94.74.63:2375"
    "us-pittsburgh" = "tcp://100.94.74.63:2375"
    "eu-helsinki"   = "tcp://100.117.102.81:2375"
    "ap-sydney"     = "tcp://100.127.2.1:2375"
    "eu-paris"      = "tcp://100.74.161.61:2375"
  }
}

data "coder_parameter" "repo_dir" {
  type = "string"
  name = "Coder Repository Directory"
  default = "~/coder"
  description = "The directory specified will be created and [coder/coder](https://github.com/coder/coder) will be automatically cloned into it 🪄."
  mutable = true
}

data "coder_parameter" "dotfiles_url" {
  type = "string"
  name = "Dotfiles URL"
  description = "A path to your dotfiles. See: https://dotfiles.github.io"
  default = " "
  mutable = true
}

data "coder_parameter" "region" {
  type = "string"
  name = "Region"
  option {
    icon = "/emojis/1f1fa-1f1f8.png"
    name = "Pittsburgh"
    value = "us-pittsburgh"
  }
  option {
    icon = "/emojis/1f1eb-1f1ee.png"
    name = "Helsinki"
    value = "eu-helsinki"
  }
  option {
    icon = "/emojis/1f1e6-1f1fa.png"
    name = "Sydney"
    value = "ap-sydney"
  }
  # option {
  #   icon = "/emojis/1f1eb-1f1f7.png"
  #   name = "Phorcys' Server in Paris"
  #   value = "eu-paris"
  # }
}

provider "docker" {
  host = lookup(local.docker_host, data.coder_parameter.region.value)
}

provider "coder" {}

data "coder_git_auth" "github" {
  id = "github"
}

data "coder_workspace" "me" {}

resource "coder_agent" "dev" {
  arch = "amd64"
  os   = "linux"

  dir = data.coder_parameter.repo_dir.value
  login_before_ready     = false
  startup_script_timeout = 60
  startup_script         = <<-EOT
    set -eux -o pipefail

    # install and start code-server
    curl -fsSL https://code-server.dev/install.sh | sh -s -- --method=standalone --prefix=/tmp/code-server --version 4.8.3
    /tmp/code-server/bin/code-server --auth none --port 13337 >/tmp/code-server.log 2>&1 &


    CODER_REPO_DIR="${data.coder_parameter.repo_dir.value}"
    if [ -n "$${CODER_REPO_DIR// }" ] && [ ! -d "$CODER_REPO_DIR" ]; then
      mkdir -p "$CODER_REPO_DIR"

      git clone https://github.com/coder/coder "$CODER_REPO_DIR"
    fi

    sudo service docker start
    DOTFILES_URI="${data.coder_parameter.dotfiles_url.value}"
    rm -f ~/.personalize.log
    if [ -n "$${DOTFILES_URI// }" ]; then
      coder dotfiles "$DOTFILES_URI" -y 2>&1 | tee -a ~/.personalize.log
    fi
    if [ -x ~/personalize ]; then
      ~/personalize 2>&1 | tee -a ~/.personalize.log
    elif [ -f ~/personalize ]; then
      echo "~/personalize is not executable, skipping..." | tee -a ~/.personalize.log
    fi
  EOT
}

resource "coder_app" "code-server" {
  agent_id     = coder_agent.dev.id
  slug         = "code-server"
  display_name = "code-server"
  url          = "http://localhost:13337/"
  icon         = "/icon/code.svg"
  subdomain    = false
  share        = "owner"

  healthcheck {
    url       = "http://localhost:13337/healthz"
    interval  = 3
    threshold = 10
  }
}

resource "docker_volume" "home_volume" {
  name = "coder-${data.coder_workspace.me.id}-home"
  # Protect the volume from being deleted due to changes in attributes.
  lifecycle {
    ignore_changes = all
  }
  # Add labels in Docker to keep track of orphan resources.
  labels {
    label = "coder.owner"
    value = data.coder_workspace.me.owner
  }
  labels {
    label = "coder.owner_id"
    value = data.coder_workspace.me.owner_id
  }
  labels {
    label = "coder.workspace_id"
    value = data.coder_workspace.me.id
  }
  # This field becomes outdated if the workspace is renamed but can
  # be useful for debugging or cleaning out dangling volumes.
  labels {
    label = "coder.workspace_name_at_creation"
    value = data.coder_workspace.me.name
  }
}

locals {
  container_name = "coder-${data.coder_workspace.me.owner}-${lower(data.coder_workspace.me.name)}"
  registry_name  = "codercom/oss-dogfood"
}
data "docker_registry_image" "dogfood" {
  name = "${local.registry_name}:main"
}

resource "docker_image" "dogfood" {
  name = "${local.registry_name}@${data.docker_registry_image.dogfood.sha256_digest}"
  pull_triggers = [
    data.docker_registry_image.dogfood.sha256_digest,
    sha1(join("", [for f in fileset(path.module, "files/*") : filesha1(f)])),
    filesha1("Dockerfile"),
  ]
  keep_locally = true
}

resource "docker_container" "workspace" {
  count = data.coder_workspace.me.start_count
  image = docker_image.dogfood.name
  name  = local.container_name
  # Hostname makes the shell more user friendly: coder@my-workspace:~$
  hostname = data.coder_workspace.me.name
  # Use the docker gateway if the access URL is 127.0.0.1
  entrypoint = ["sh", "-c", replace(coder_agent.dev.init_script, "/localhost|127\\.0\\.0\\.1/", "host.docker.internal")]
  # CPU limits are unnecessary since Docker will load balance automatically
  memory  = 32768
  runtime = "sysbox-runc"
  env = [
    "CODER_AGENT_TOKEN=${coder_agent.dev.token}",
  ]
  host {
    host = "host.docker.internal"
    ip   = "host-gateway"
  }
  volumes {
    container_path = "/home/coder/"
    volume_name    = docker_volume.home_volume.name
    read_only      = false
  }
  # Add labels in Docker to keep track of orphan resources.
  labels {
    label = "coder.owner"
    value = data.coder_workspace.me.owner
  }
  labels {
    label = "coder.owner_id"
    value = data.coder_workspace.me.owner_id
  }
  labels {
    label = "coder.workspace_id"
    value = data.coder_workspace.me.id
  }
  labels {
    label = "coder.workspace_name"
    value = data.coder_workspace.me.name
  }
}

resource "coder_metadata" "container_info" {
  count       = data.coder_workspace.me.start_count
  resource_id = docker_container.workspace[0].id
  item {
    key   = "memory"
    value = docker_container.workspace[0].memory
  }
  item {
    key   = "runtime"
    value = docker_container.workspace[0].runtime
  }
}

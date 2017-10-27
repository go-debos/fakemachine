pipeline {
  agent {
    dockerfile {
      args '--device=/dev/kvm --group-add kvm -v /etc/group:/etc/group'
    }
  }
  environment {
    GOPATH="${env.WORKSPACE}/.gopath"
  }
  stages {
    stage("Setup path") {
      steps {
        sh "mkdir -p .gopath/src/github.com/go-debos"
        sh "ln -sf ${env.WORKSPACE} .gopath/src/github.com/go-debos/fakemachine"
        sh "go get -d ./..."
      }
    }
    stage("Run test") {
      steps {
        sh "go test -v"
      }
    }
  }
}

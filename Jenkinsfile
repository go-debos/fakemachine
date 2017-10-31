pipeline {
  agent {
    dockerfile {
      args '--device=/dev/kvm'
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
        sh "go get -v -t -d ./..."
      }
    }
    stage("Run test") {
      steps {
        sh "go test -v"
      }
    }

    stage("Test build cmd") {
      steps {
        sh "go install github.com/go-debos/fakemachine/cmd/fakemachine"
      }
    }
  }
}

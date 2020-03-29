FROM buildpack-deps:disco

### base ###
RUN yes | unminimize \
    && apt-get install -yq \
        zip \
        unzip \
        bash-completion \
        build-essential \
        htop \
        jq \
        less \
        locales \
        man-db \
        nano \
        software-properties-common \
        sudo \
        time \
        vim \
        multitail \
        lsof \
    && locale-gen en_US.UTF-8 \
    && mkdir /var/lib/apt/dazzle-marks \
    && apt-get clean && rm -rf /var/lib/apt/lists/* /tmp/*

ENV LANG=en_US.UTF-8

### Git ###
RUN add-apt-repository -y ppa:git-core/ppa \
    && apt-get install -yq git \
    && rm -rf /var/lib/apt/lists/*

### Gitpod user ###
# '-l': see https://docs.docker.com/develop/develop-images/dockerfile_best-practices/#user
RUN useradd -l -u 33333 -G sudo -md /home/gitpod -s /bin/bash -p gitpod gitpod \
    # passwordless sudo for users in the 'sudo' group
    && sed -i.bkp -e 's/%sudo\s\+ALL=(ALL\(:ALL\)\?)\s\+ALL/%sudo ALL=NOPASSWD:ALL/g' /etc/sudoers
ENV HOME=/home/gitpod
WORKDIR $HOME
# custom Bash prompt
RUN { echo && echo "PS1='\[\e]0;\u \w\a\]\[\033[01;32m\]\u\[\033[00m\] \[\033[01;34m\]\w\[\033[00m\] \\\$ '" ; } >> .bashrc

### Gitpod user (2) ###
USER gitpod
# use sudo so that user does not get sudo usage info on (the first) login
RUN sudo echo "Running 'sudo' for Gitpod: success" && \
    # create .bashrc.d folder and source it in the bashrc
    mkdir /home/gitpod/.bashrc.d && \
    (echo; echo "for i in \$(ls \$HOME/.bashrc.d/*); do source \$i; done"; echo) >> /home/gitpod/.bashrc

### Install C/C++ compiler and associated tools ###
LABEL dazzle/layer=lang-c
LABEL dazzle/test=tests/lang-c.yaml
USER root
RUN curl -fsSL https://apt.llvm.org/llvm-snapshot.gpg.key | apt-key add - \
    && echo "deb https://apt.llvm.org/disco/ llvm-toolchain-disco main" >> /etc/apt/sources.list.d/llvm.list \
    && apt-get update \
    && apt-get install -yq \
        clang-format \
        clang-tidy \
        # clang-tools \ # breaks the build atm
        clangd \
        gdb \
        lld \
    && cp /var/lib/dpkg/status /var/lib/apt/dazzle-marks/lang-c.status \
    && apt-get clean && rm -rf /var/lib/apt/lists/* /tmp/*

### Go ###
LABEL dazzle/layer=lang-go
LABEL dazzle/test=tests/lang-go.yaml
USER gitpod
ENV GO_VERSION=1.14 \
    GOPATH=$HOME/go-packages \
    GOROOT=$HOME/go
ENV PATH=$GOROOT/bin:$GOPATH/bin:$PATH
RUN curl -fsSL https://storage.googleapis.com/golang/go$GO_VERSION.linux-amd64.tar.gz | tar xzs
# user Go packages
ENV GOPATH=/workspace/go \
    PATH=/workspace/go/bin:$PATH

### Docker client ###
USER root
RUN curl -fsSL https://download.docker.com/linux/ubuntu/gpg | apt-key add - \
    # 'cosmic' not supported
    && add-apt-repository -yu "deb [arch=amd64] https://download.docker.com/linux/ubuntu bionic stable" \
    && apt-get install -yq docker-ce-cli=5:18.09.0~3-0~ubuntu-bionic \
    && apt-get clean && rm -rf /var/lib/apt/lists/* /tmp/*

### Kubectl & Helm To be added for deployment


USER gitpod

FROM gcr.io/google-containers/fluentd-gcp:2.0.18

# install additional plugins.
RUN BUILD_DEPS="make gcc g++ libc6-dev ruby-dev libffi-dev" \
    && clean-install $BUILD_DEPS \
    && echo "gem 'fluent-plugin-influxdb', '~>1.1.0'" >> Gemfile \
    && echo "gem 'fluent-plugin-kubernetes_metadata_filter', '~>1.2.1'" >> Gemfile \
    && echo "gem 'fluent-plugin-flatten-hash', '~>0.4.1'" >> Gemfile \
    && gem install --file Gemfile \
    && apt-get purge -y --auto-remove \
                     -o APT::AutoRemove::RecommendsImportant=false \
                     $BUILD_DEPS \
    && rm -rf /tmp/* \
              /var/lib/apt/lists/* \
              /usr/lib/ruby/gems/*/cache/*.gem \
              /var/log/* \
              /var/tmp/*

# Now, we use configmap to mount fluent.conf into contaienr,
# so comment out this line. For config details, please check
# charts/fission-all/config/fluentd.conf
# COPY fluent.conf /etc/fluent/fluent.conf

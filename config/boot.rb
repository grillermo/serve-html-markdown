ENV["BUNDLE_GEMFILE"] ||= File.expand_path("../Gemfile", __dir__)

Process.setproctitle("serve-html-markdown")

require "bundler/setup" # Set up gems listed in the Gemfile.

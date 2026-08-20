ENV["RAILS_ENV"] ||= "test"
ENV["VIDEOS_DIR"] ||= File.expand_path("../tmp/test-videos", __dir__)
require_relative "../config/environment"
require "rails/test_help"

# Rails 8 lazy routes must load before Devise 4.9 test helpers resolve mappings.
Rails.application.routes_reloader.execute_unless_loaded

module ActiveSupport
  class TestCase
    # Serial by default: forked workers segfault in pg while building their per-worker
    # test database (ActiveRecord::TestDatabases#create_and_load_schema). The suite runs
    # in ~1s anyway. Override with PARALLEL_WORKERS to opt back in.
    parallelize(workers: ENV.fetch("PARALLEL_WORKERS", 1).to_i)

    # Add more helper methods to be used by all tests here...
  end
end

class ActionDispatch::IntegrationTest
  include Devise::Test::IntegrationHelpers
end

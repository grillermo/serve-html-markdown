require "test_helper"

class UserTest < ActiveSupport::TestCase
  test "authenticates with a valid password" do
    user = User.create!(email: "admin@example.com", password: "s3cretpass")

    assert user.valid_password?("s3cretpass")
    assert_not user.valid_password?("wrong")
  end

  test "requires an email" do
    user = User.new(password: "s3cretpass")

    assert_not user.valid?
  end

  test "defaults the remembered expansion mode to create_new" do
    user = User.create!(email: "prefs@example.com", password: "s3cretpass")

    assert_equal "create_new", user.expansion_mode
  end

  test "rejects an unknown remembered expansion mode" do
    user = User.new(email: "badprefs@example.com", password: "s3cretpass", expansion_mode: "nonsense")

    assert_not user.valid?
  end
end
